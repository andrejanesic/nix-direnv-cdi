# nix-direnv-cdi — Implementation Plan

## 0. One-line goal

A small **Go** program that makes a project's **nix-direnv dev-shell** available inside **any OCI container** (podman, docker) via **one generic CDI device** you attach with a single `--device`. The device carries *no* project data; a `createRuntime` hook injects the dev-shell **dynamically at container-creation time** from the **loaded direnv environment it inherits**. One device serves every project — nothing per-project is registered.

You attach it with: `podman run --device nix-direnv.cdi/shell=devshell <image> <cmd>` (the ref is a constant, exported as `$DIRENV_CDI`).

> **Architecture pivot (2026-06-14).** This supersedes the original *static* design — one baked CDI spec per project, named by a fingerprint, carrying the closure mounts + dev-shell env in the spec. That design worked and was fully tested (old milestones 1–8), but it meant **N projects → N registered devices**, which we did not want. We pivoted to a **single generic device + a dynamic hook** after empirically verifying (see §1) that a `createRuntime` hook can inject the closure mounts at run time by entering the container's mount namespace. The git history retains the static implementation; this plan describes the refactor to the dynamic design.

**End-to-end flow (the simplified model):**
1. **Once per machine:** `nix-direnv-cdi install` — registers the one generic device with podman/docker.
2. **Per project** (in `.envrc`, right after `use flake`): `nix-direnv-cdi gen` writes `.direnv/cdi/mounts.json` (the closure) and exports the constant `DIRENV_CDI=nix-direnv.cdi/shell=devshell`. Re-runs automatically on every direnv reload, so it stays fresh as dependencies change.
3. **Run** (from the loaded dev-shell): `podman run --device "$DIRENV_CDI" <image> <cmd>` → the hook gates on `DIRENV_DIR`, bind-mounts the project's closure into the container (read-only), and makes `PATH`/env additive. Outside the loaded shell the device is **inert**.

---

## 1. Background — distilled findings (the constraints that shaped the design)

These are load-bearing facts. Items marked **VERIFIED** were confirmed empirically in this environment (rootless podman/crun, and rootless runc via a standalone bundle).

**CDI / OCI capabilities**
- A CDI device's `containerEdits` can carry `env`, `mounts`, `deviceNodes`, and `hooks`. `env` is **literal set-only** (no append, no `$VAR` expansion). OCI **hooks cannot modify `process.env`/`process.args`**. → **additive `PATH` cannot be expressed as data; it requires wrapping the entrypoint binary** (unchanged from the original design).
- A CDI device's static `mounts` are applied by the runtime **before any hook runs**, and a device is selected **purely by its `kind=name`** (before the hook exists). So *per-project mounts in the spec ⇒ per-project device names ⇒ a fingerprint*. The only way to avoid per-project devices while keeping surgical (not whole-`/nix/store`) mounts is to **inject the mounts from the hook** instead of listing them in the spec.

**Hook lifecycle & dynamic mount injection (the core enabling finding)**
- `createRuntime` runs **in the host (runtime) namespace, after mounts are performed, before `pivot_root`**. It can read `config.json` and the OCI **State** (which carries the container `pid` and `bundle`/`root`), and write the rootfs.
- **VERIFIED:** a `createRuntime` hook **cannot** make a *host-side* `mount --bind` appear in the container (the container's mount ns is `MS_PRIVATE|MS_REC` — host mounts don't propagate in). Only file/dir *writes* into the rootfs propagate.
- **VERIFIED:** a `createRuntime` hook **can** inject mounts by **entering the container's mount namespace** (via the container `pid` from State) and mounting there. The container then sees the mount (read + execute confirmed).
  - **crun / rootless podman:** the hook runs as subuid-root *inside the container's own userns* → `setns(CLONE_NEWNS)` then `mount` works; entering the userns is rejected/unneeded.
  - **rootless runc, unprivileged invoker (`CapEff=0`, container in a child userns):** `setns(mnt)` fails `EPERM`; must `setns(CLONE_NEWUSER)` *first* (to gain caps in the child userns), then `setns(CLONE_NEWNS)`, then `mount`. **VERIFIED works.**
  - **Robust strategy:** try mount-ns entry; on `EPERM`, fall back to userns+mount-ns entry. **VERIFIED necessary (runc) and sufficient (both).** (The shipped Go hook implements only the mount-ns entry; the `CLONE_NEWUSER` fallback can't be done in pure Go, and bare rootless runc is **out of scope** — see §6 R3 and §7.)
  - Bind source paths (host `/nix/store/X`) are reachable from inside the container mount ns **pre-`pivot_root`**, and mounts created under `root.path` survive `pivot_root` → appear at `/...`. So plain `setns`+`mount --bind` suffices — no `open_tree`/`move_mount` needed.
  - **Prior art:** NVIDIA's `libnvidia-container` injects its mounts the same way — entering the container mount ns (`ns_enter(..., CLONE_NEWNS)` in `nvc_mount.c`). It does **only** `CLONE_NEWNS` (no userns entry), which is exactly why rootless GPU has historically been painful; our userns fallback closes that gap.
  - Because the hook is **OCI-standard** and embedded in the CDI device, it runs under **both crun and runc** → the device is cross-runtime. Real moby+CDI end-to-end is **not yet tested** (deferred smoke test); runc itself is verified.

**Reading the dev-shell at runtime (instead of baking it)**
- The hook is launched from the user's **loaded dev-shell**, so it **inherits that environment**. **VERIFIED** that arbitrary inherited vars (incl. `DIRENV_DIR`, `DIRENV_DIFF`) reach the hook. (Note: the hook's own `PATH` is sanitized by the runtime, so use absolute paths / direct syscalls — but `DIRENV_DIFF` etc. pass through intact.)
- `DIRENV_DIR` → the **gate** (present = the user is in an approved dev-shell; this *is* the authorization — being in the shell means they ran `direnv allow`) and **locates the project** (and its `mounts.json`).
- `DIRENV_DIFF` → decode (padded URL-safe base64 → zlib → JSON `{"p":prev,"n":next}`) for the additive-`PATH` **prefix** and the dev-shell **env vars**, live. `DIRENV_DIFF` is unset during `.envrc` evaluation and only exists in the finalized/loaded env — which is exactly when the hook runs, so runtime is the correct place to read it.
- The **closure** (which `/nix/store` paths to mount) comes from the project's gcroot (`.direnv/flake-profile-*` → `nix-store -qR`), **pre-computed by `gen`** into `.direnv/cdi/mounts.json`. The gcroot exists during `.envrc` evaluation and needs no `DIRENV_DIFF`, so `gen` can run inside `.envrc` right after `use flake`.

**Entrypoint resolution (additive PATH — unchanged)**
- crun execs a **relative** `args[0]` via `PATH` search but an **absolute** one directly. The dev-shell prefix isn't on the image `PATH` at hook time. So additive `PATH` is achieved by wrapping the entrypoint: relative → shadow shim in the first image-`PATH` dir; absolute-in-writable-rootfs → wrap in place. Wrapper: `#!/bin/sh\nexport PATH="<prefix>:$PATH"\n<dev-shell env exports>\nexec "<real>" "$@"`.
- **Known limitation (T9):** an absolute path into a read-only mounted prefix runs but can't be made additive. Mitigation: run tools by name.

**config.json / OCI field names** — `config.json` is the OCI spec: mounts use `.destination`/`.source`, command is `.process.args`, env `.process.env`, rootfs `.root.path`. The State JSON on stdin carries `pid`, `bundle`, and (podman/crun) `root`.

**Gotchas**
- CDI spec dir + the hook binary path must be traversable (≥`0755`); a `0700` parent → "unresolvable CDI devices" under rootless podman.
- The hook must be **best-effort: any failure → exit 0** (a non-zero `createRuntime` hook aborts the container — **VERIFIED**).
- The CDI device is **opt-in per `--device`** — the hook does *not* run on unrelated containers (unlike a `when: always` podman `precreate` hook), so there's no blast radius.

---

## 2. What `install` registers (the one generic device)

A **single** device, identical for every project, written to `~/.config/cdi/nix-direnv.json` and registered with podman/docker:

```jsonc
{
  "cdiVersion": "<min required>",
  "kind": "nix-direnv.cdi/shell",
  "devices": [
    { "name": "devshell",
      "containerEdits": {
        "hooks": [
          { "hookName": "createRuntime",
            "path": "<abs path to the installed nix-direnv-cdi>",
            "args": ["nix-direnv-cdi", "hook"] }
        ]
      } }
  ]
}
```

- **No `env`, no `mounts`, no fingerprint.** The device only installs the hook.
- Reference: `--device nix-direnv.cdi/shell=devshell`; exported as `$DIRENV_CDI` (a **constant** for all projects).
- The hook `path` resolves to the installed binary via `os.Executable()` (immutable nix-store path when installed via the flake — see packaging).

**Per-project data** (written by `gen`, *not* registered, gitignored): `<project>/.direnv/cdi/mounts.json` — the closure path list the hook mounts.

---

## 3. Program structure (Go)

```
nix-direnv-cdi/
  main.go                 # subcommand dispatch
  internal/
    devshell/             # decode DIRENV_DIFF (prefix+env); resolve gcroot + closure (nix-store -qR)
    cdispec/              # build & validate the single generic device
    hook/                 # createRuntime: gate, mount-inject (ns-entry), wrap entrypoint
    nsmount/              # ns-entry + bind-mount helper (x/sys/unix Setns+Mount; mnt / userns+mnt)
    ociconfig/            # OCI State + config.json helpers
    install/              # register the generic device with podman/docker (backup-then-auto)
  testdata/ , *_test.go
  flake.nix               # build + nix run / profile install
```
(The `fingerprint` package and the placement-mode logic are **deleted**.)

**Subcommands**
- `install` — write the single generic CDI device to `~/.config/cdi` (hook `path` = installed binary; dir chmod `0755`) and register the dir in podman's `containers.conf.d` drop-in and docker's `daemon.json` (existing backup-then-auto logic, with manual-instructions fallback). One-time.
- `gen` — resolve the gcroot from `.direnv/flake-profile-*`, compute the closure (`nix-store -qR`), write `<project>/.direnv/cdi/mounts.json`; print the constant `$DIRENV_CDI`. **Runs inside `.envrc`** after `use flake` (no `DIRENV_DIFF` needed). Re-run when dependencies change (a direnv reload does this automatically).
- `hook` — the `createRuntime` hook. Best-effort, always exit 0:
  1. **Gate:** read `DIRENV_DIR` from the inherited env. Absent → exit 0 (no-op; the device is inert outside an approved shell).
  2. **Mounts:** read `<DIRENV_DIR>/.direnv/cdi/mounts.json` — the **closure only**; the old design's `rw` project-root/workdir mount is **dropped** (mount your sources yourself with `-v $PWD:$PWD` if you want them). For each closure path, enter the container's mount ns (try `setns(mnt)`; on `EPERM`, `setns(user)`+`setns(mnt)`) and bind it **read-only** onto `<rootfs><path>` (mkdir the target first). `DIRENV_DIR` carries direnv's leading `-` marker, which is stripped before use.
  3. **PATH/env:** decode `DIRENV_DIFF` for the prefix + env vars; wrap the entrypoint (shim) so `PATH` is additive and the dev-shell env vars are exported.
- `version`.

**Libraries:** `opencontainers/runtime-spec/specs-go` (State + config.json), `tags.cncf.io/container-device-interface` (build/validate the generic spec), `golang.org/x/sys/unix` (`Setns`, `Mount`, `Open` of `/proc/<pid>/ns/{user,mnt}`).

---

## 4. Behaviours to achieve / preserve

Additive-`PATH` matrix (ported from the MVP `../cdi-additive-test.sh`, still via entrypoint wrapping):

| | behaviour |
|-----|------------------------------|
| T1 | entrypoint=shell → `PATH` additive (prefix prepended, image base preserved) |
| T2 | a dev-shell-only tool is reachable via the additive `PATH` |
| T3 | base-image tools still resolve |
| T4 | the wrapped entrypoint execs the real binary |
| T5 | works for `sh` — no shebang recursion |
| T6 | control: **without** the device, `PATH` is the plain image default |
| T7 | a dev-shell-only tool as the **bare** entrypoint runs |
| T8 | additive `PATH` holds even when the entry is a dev-shell-only tool |
| T9 | limitation: absolute path into a RO mount runs but is **not** additive |
| T10 | absolute path into the writable rootfs **is** wrapped |

Plus the new, dynamic-design behaviours:
- **Dynamic mounts:** the project closure is bind-mounted by the hook (not the spec) and dev-shell tools run.
- **Gate:** passing `--device` **outside** an approved dev-shell (no `DIRENV_DIR`) is **inert** — no mounts, `PATH`/env untouched.
- **One device, many projects:** the same `nix-direnv.cdi/shell=devshell` works for any project; the right closure is chosen at run time.
- **Cross-runtime ns-entry:** mnt-only entry (crun/podman) and userns+mnt fallback (runc) both produce a container-visible mount.

---

## 5. Test plan

**Tier A — unit (always, no container)**
- `cdispec`: builds a valid **generic** device (passes the CNCF validator); exactly one `createRuntime` hook; no env/mounts.
- `gen`/`devshell`: closure written to `mounts.json` from a faked gcroot; `DIRENV_DIFF` decode (prefix/env).
- `hook` pure logic: gate decision (DIRENV_DIR present/absent), wrapper content, mount-target path computation, mounts.json parsing.

**Tier B — synthetic integration (podman, no nix)**
- Register the generic device; fabricate a fake "project": a temp dir with a fake prefix tool, a `mounts.json` pointing at it, and a synthetic `DIRENV_DIR`/`DIRENV_DIFF` in the env. `podman run --device …` → the fake tool runs inside the container (dynamic mount + additive PATH). Includes the **gate** test (no `DIRENV_DIR` → inert) and the T1–T10 matrix.

**Tier C — real-flake smoke (nix-gated)**
- Committed `testdata/fixture` (`flake.nix` providing `hello`, `.envrc` with `use flake` + `gen`). `install` the generic device, then `direnv exec <fixture> -- podman run --device "$DIRENV_CDI" busybox hello` → `Hello, world!`, `PATH` additive; control without the device shows `hello` absent.

**Deferred:** a real **moby+CDI** end-to-end (`docker run --device`) on a host with real Docker — the runtime-level behaviour is already verified on runc.

---

## 6. Milestones (refactor)

- **✅ R1. PLAN rewrite** — this document.
- **✅ R2+R4. Producer side** — `cdispec.Build` produces the one generic device (hook only); `install` writes+registers it (hook `path` = the installed binary); `gen` writes the closure to `.direnv/cdi/mounts.json` from the gcroot (no `DIRENV_DIFF`, runnable in `.envrc`) and prints the constant `$DIRENV_CDI`. Combined because the new `gen` no longer touches `cdispec`. `fingerprint`/placement modes deleted. Tier A.
- **✅ R3. `nsmount` + `hook`** — `hook` gates on `DIRENV_DIR`, injects the closure via `nsmount`, and wraps the entrypoint for additive `PATH` + dev-shell env from the inherited `DIRENV_DIFF`. `NDC_HOOK_LOG` enables opt-in debug logging.
  - **`nsmount` does mount-ns-only entry** (`unshare(CLONE_FS)` — required, since Go runtime threads share `CLONE_FS` and that makes `setns(CLONE_NEWNS)` fail `EINVAL` — then `setns(CLONE_NEWNS)` + bind, on a dedicated locked-and-discarded thread). This covers **every real configuration**: rootless podman/crun (verified), rootful podman/root, rootful docker, and rootless docker (RootlessKit). **Bare rootless runc with an unprivileged invoker is out of scope** (§7): there `setns(CLONE_NEWNS)` returns `EPERM` and the only fix is a `CLONE_NEWUSER` entry, which pure Go can't do (multithreaded `setns(CLONE_NEWUSER)` is disallowed — would need an `nsexec`-style C constructor). The hook degrades gracefully there: the mount is skipped (best-effort) and the container still runs without the dev-shell. The userns fallback is verified feasible via `nsenter`, should we ever want that target.
  - Mounts are file- or dir-typed to match the source; read-only is **best-effort** (rootless refuses the ro-remount; store paths are immutable `0555`).
- **✅ R5. Tier B** — synthetic, nix-free dynamic-mount + gate matrix on podman.
- **✅ R6. Tier C** — real-flake end-to-end (`install`/`gen`/`--device`, hello propagates, PATH additive).
- **✅ R7. direnv integration + docs** — `.envrc` snippet (`use flake`; `eval "$(nix-direnv-cdi gen)"`) and the `contrib/use_cdi.sh` `use cdi` helper (both verified to run inside `.envrc`); `gen` stdout made eval-clean; README. **Deferred:** the real moby+CDI smoke test (this environment's `docker` is a podman shim).
- **✅ Cleanup** — `fingerprint`, placement modes, and the static Tier A/B/C tests removed (in R2/R3).

---

## 7. Non-goals & notes

- **Non-goals:** mounting the entire `/nix/store` (rejected — surgical closure only); the OCI runtime-shim/`precreate` approaches (podman-only or heavier; we stay on standard CDI hooks); prefixes that are not host-accessible bind mounts; **bare rootless runc invoked by an unprivileged user** (no outer privileged userns) — out of scope, as it would need a C `nsexec` constructor for the userns entry; the hook no-ops gracefully there. Every real podman/docker configuration (rootless/rootful, crun/runc) is covered by the mount-ns-only path.
- **Known limitation:** absolute path into a RO-mounted prefix (T9) — runs, not additive.
- **Authorization model:** being in the loaded dev-shell *is* the gate. Outside it, the device is inert. We deliberately do **not** make the dev-shell available without the user having entered (and thus approved) it.
- **Cross-runtime status:** verified on crun (podman) and runc; real Docker end-to-end deferred to a one-off smoke test.
- **Retired design:** the static per-project spec + fingerprint + shared/local placement is superseded but preserved in git history; `../cdi-additive-test.sh` remains the executable reference for the additive-PATH behaviour.
