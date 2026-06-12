# nix-direnv-cdi — Implementation Plan

## 0. One-line goal

A small **Go** program that makes a project's **nix-direnv dev-shell** available inside **any OCI container** (podman, docker, or `docker compose`) by generating a **CDI device** — read-only closure mounts + dev-shell env + a `createRuntime` hook that gives **additive `PATH`** — with **no custom launcher** and a **stock base image**. You attach it with a single `--device`.

The behaviour is already proven end-to-end by the MVP bash script `../cdi-additive-test.sh` (13/13). This plan ports that mechanism to a maintainable Go binary plus a real test suite that drives a container runtime to confirm the dev-shell actually propagates.

---

## 1. Background — distilled findings (the constraints that shaped the design)

These are the hard-won facts from the exploration. They are load-bearing; the design follows from them.

**CDI / OCI capabilities**
- A CDI spec's `containerEdits` can carry `env`, `mounts`, `deviceNodes`, and `hooks`. There is **no `process.args`/entrypoint edit**, and `env` is **literal set-only** — no append, no variable expansion (verified against the CDI SPEC).
- OCI **hooks cannot modify `process.env` or `process.args`** before the user process executes — confirmed verbatim in the OCI runtime-spec. Hooks get container *state* on stdin and signal via exit code; there is no channel to mutate the process spec.
- Therefore **additive `PATH` cannot be expressed as pure CDI data.** The only way to append to the image's real `PATH` is to **wrap the entrypoint binary**, which requires a hook that edits the rootfs.

**Hook lifecycle (why `createRuntime`)**
- `createRuntime` runs **in the host (runtime) namespace, before `pivot_root`**. It is the one stage that can both **read `config.json`** (find `process.args[0]`, the env, the rootfs path) **and write the rootfs** (the mount namespace is created and mounts performed). Verified: a `createRuntime` hook reads the entrypoint and writes a file the main process then sees.
- `createContainer` runs pre-pivot in the *container* ns → its writes don't reach the final rootfs (our first failed test used it).
- The `createRuntime` filesystem guarantee is **spec-"underspecified"** ("only expect the mount namespace created and mounts performed") but works on **crun (podman)** and **runc (docker)** in practice — that's the de-facto behaviour we rely on.
- CDI-embedded hooks become `config.json` hooks → they run under **both crun and runc**, so the mechanism is cross-runtime (unlike user `hooks.d`, which is podman/CRI-O only).

**Entrypoint resolution (the subtle part)**
- crun execs a **relative** `args[0]` via PATH search, but an **absolute** `args[0]` directly (no PATH search).
- The dev-shell prefix is **not on the image PATH** at hook time, and the prefix's **bind-mounts are not visible in the host namespace** where the hook runs.
- So the hook must:
  1. Resolve `args[0]` `command -v`-style across **`prefix : imagePATH`**, mapping each candidate **container path → host-accessible path** via the OCI mounts (`.destination`/`.source`). For a nix store the source == destination path, so this is near-identity.
  2. For a **relative** entrypoint: drop a **shadow shim** named `args[0]` into the **first image-PATH dir** (e.g. `/usr/local/sbin`) so crun finds it first. This avoids `mv`, RO-mount issues, and shebang recursion (the shim's `#!/bin/sh` resolves to the untouched `/bin/sh`).
  3. For an **absolute** entrypoint **inside the writable rootfs**: wrap in place (move real aside, write wrapper).
  4. The wrapper is `#!/bin/sh\nexport PATH="$prefix:$PATH"\nexec "<real>" "$@"`.
- **Known limitation (T9):** an **absolute path into a read-only mounted prefix** (e.g. `podman run img /nix/store/.../bin/tool`) *runs* but **cannot be made additive** — crun execs the absolute path directly (no shadow possible) and the binary is RO + invisible to the host-ns hook. Mitigation: run the tool **by name**, not by full store path.

**config.json field names**
- `config.json` is the **OCI** spec → mounts use `.destination`/`.source`, command is `.process.args`, env is `.process.env`, rootfs is `.root.path`. (CDI's `hostPath`/`containerPath` only exist in the *CDI* spec before the runtime translates it.) Getting this wrong was one of the MVP bugs.

**Cross-runtime placement**
- A CDI device is referenced as `--device <vendor>/<class>=<name>`.
- **Docker reads CDI specs only from daemon-configured dirs** (`cdi-spec-dirs` in `daemon.json`, default `/etc/cdi` + `/var/run/cdi`); there is **no per-`docker run` spec-dir flag**, and CDI is enabled by default since Docker 28.3 (opt-in 25.0–28.1).
- **Podman has `--cdi-spec-dir` per command** and `cdi_spec_dirs` in `containers.conf`.
- Consequence: a **per-project spec dir** works only for podman; **cross-runtime requires a shared, registered dir with the identity in the device name (a fingerprint).** Hence we support **both** placement modes.

**Gotchas to bake in**
- CDI spec dir + every path it references (hook binary, mount sources) must be **traversable** (≥ `0755`); a `0700` parent yields `unresolvable CDI devices` under rootless podman.
- The MVP's bug parade — jq `startswith` evaluating against the wrong input, `IFS=:` vs command-substitution, OCI-vs-CDI field names, off-by-one env stripping — were all **bash/jq accidental complexity**. The Go port with typed OCI/CDI structs eliminates that entire class.

**Precedent**
- NVIDIA's Container Toolkit (the canonical CDI-spec generator + CDI hook + runtime) is **Go (90.8%)**, using `opencontainers/runtime-spec/specs-go` and `tags.cncf.io/container-device-interface`. We use the same rails.

---

## 2. What the generated CDI spec contains

`kind: nix-direnv.cdi/shell`, one device per project. `containerEdits`:

- **`mounts`** — every path from the dev-shell **closure** (`nix-store -qR` over the gcroot) as `ro,bind`; plus the **project/workdir** as `rw,bind`. For nix, `source == destination`.
- **`env`** — all exported dev-shell vars from the direnv diff **except `PATH`**, set literally; plus `DEVSHELL_PREFIX=<colon-joined nix-store bin dirs>`. (`PATH` is deliberately *not* set; the hook makes it additive.)
- **`hooks`** — exactly one:
  `{ "hookName": "createRuntime", "path": "<abs path to nix-direnv-cdi>", "args": ["nix-direnv-cdi", "hook"] }`

Identity & placement (mode-dependent):
- **shared** (default): write `~/.config/cdi/nix-direnv-<hash>.json`, device name `<hash>` = fingerprint of the project root (`${DIRENV_DIR#-}`); reference `--device nix-direnv.cdi/shell=<hash>`. Requires a one-time registration of `~/.config/cdi` in `containers.conf` (podman) and/or `daemon.json` (docker). Works across podman/docker/compose. Exported as `$DIRENV_CDI`.
- **local**: write `$PWD/.direnv/cdi/devshell.json`, constant device name `shell`; reference `--cdi-spec-dir $PWD/.direnv/cdi --device nix-direnv.cdi/shell=shell`. Gitignored, no registration; podman-only.

---

## 3. Program structure (Go)

Single static binary `nix-direnv-cdi`, no runtime deps (no jq, no bash).

```
nix-direnv-cdi/
  go.mod
  main.go                 # subcommand dispatch
  internal/
    devshell/             # discover prefix + env vars (loaded env) and closure (gcroot + nix-store -qR)
    cdispec/              # build & VALIDATE the CDI spec via tags.cncf.io/container-device-interface
    hook/                 # the createRuntime wrap logic, typed against specs-go
    ociconfig/            # thin helpers over opencontainers/runtime-spec/specs-go
    fingerprint/          # stable per-project id from the project root
  testdata/ , *_test.go
  flake.nix               # build + distribute (nix run / profile install)
  PLAN.md
```

**Subcommands**
- `gen [--mode shared|local] [--out <dir>]` — discover the dev-shell, compute the fingerprint, build+validate the CDI spec, write it, and print the device reference (and `export DIRENV_CDI=...` line for `eval`).
  - **Input (current approach):** read the nix-store `PATH` prefix and the exported dev-shell vars from the **loaded direnv environment** (`os.Environ`); walk the **closure** from `.direnv/flake-profile-*` via `nix-store -qR` (shell out). Project root from `${DIRENV_DIR#-}` (fallback `$PWD`).
- `hook` — the `createRuntime` hook: read OCI state on stdin → bundle → parse `config.json` into `specs.Spec` → wrap the entrypoint (algorithm in §1). **Best-effort: any error → exit 0**, never break the container.
- `install` (optional) — idempotently register the shared CDI dir in `containers.conf`/`daemon.json`; print manual steps if it can't.
- `version`.

**Libraries**
- `github.com/opencontainers/runtime-spec/specs-go` — parse `config.json` (`spec.Process.Args`, `spec.Mounts[].Destination/.Source`, `spec.Root.Path`).
- `tags.cncf.io/container-device-interface` — build and **validate** the CDI spec (don't hand-roll JSON).

---

## 4. Behaviours to preserve (port the MVP's 13 assertions)

The Go version must reproduce `../cdi-additive-test.sh` exactly, including the documented limitation:

| MVP | behaviour the port must keep |
|-----|------------------------------|
| T1 | entrypoint=shell → `PATH` is **additive** (prefix prepended, image base preserved) |
| T2 | a dev-shell-only tool is reachable via the additive `PATH` |
| T3 | base-image tools still resolve (not overridden) |
| T4 | the wrapped entrypoint execs the **real** binary correctly |
| T5 | works for a different entrypoint (`sh`) — no shebang recursion |
| T6 | control: **without** the device, `PATH` is the plain image default (no leak) |
| T7 | a dev-shell-only tool as the **bare** entrypoint runs (prefix resolution + shadow shim) |
| T8 | additive `PATH` holds even when the entry is a dev-shell-only tool |
| T9 | **limitation:** absolute path into a RO mount **runs but is NOT additive** (assert the non-additive behaviour) |
| T10 | absolute path into the **writable image rootfs** **is** wrapped (additive) |

Plus the gotchas: `0755` traversability of spec/hook/source paths; OCI field names; best-effort `exit 0`.

---

## 5. Test plan

Three tiers. Runtime selection: **detect** which CDI-capable runtime(s) are present (real `dockerd` with CDI enabled, and/or `podman`), run the matrix that's available, and **`t.Skip` (not fail)** when none supports CDI. "docker" may be a real engine or the podman-backed `docker` shim — detect and use whatever resolves.

**Tier A — unit (Go, always, no container)**
- `cdispec`: builds a spec that **passes the CNCF validator**; correct kind/name/mounts/env/hook; `DEVSHELL_PREFIX` set, `PATH` *not* set.
- `hook`: given a synthetic `config.json` + a temp rootfs, the wrap logic writes the correct shim, resolves relative entrypoints across `prefix:imagePATH`, maps container→host via mounts, handles absolute-in-rootfs vs absolute-in-RO, and never errors out (exit 0).
- `fingerprint`: deterministic, stable, CDI-name-valid (no slashes/`=`); different roots → different ids.
- `devshell`: prefix/closure extraction from a faked environment + a faked gcroot.

**Tier B — integration with a synthetic prefix (always, needs a CDI runtime)**
- Mirrors the MVP: build the binary, `gen` a spec whose "prefix" is a fake bin dir with a marker tool, then **`<runtime> run --device ...`** and assert the propagation, for **both placement modes** (shared + local) and **each detected runtime**:
  - The marker tool (present **only** in the dev-shell prefix) **runs inside the container** → this is the core "direnv propagated" assertion.
  - `PATH` contains the prefix **and** the image base (additive).
  - Base tools still work; control run without the device shows no leak; T9/T10 absolute cases behave as documented.
- Hermetic; **no nix required**. This tier is the regression net for the mechanism.

**Tier C — real-flake smoke (gated on `nix` being present)**
- A committed `testdata/fixture/{flake.nix,.envrc}` whose `devShell` provides a known, non-base tool (e.g. `hello`/`cowsay`).
- Materialise `.direnv` via `nix-direnv`, `gen` from it, propagate into a container via the detected runtime, and assert the **real nix tool** runs inside the container and `PATH` is additive. Skipped with a clear message if `nix` is absent.

**"Call docker to check direnv is propagated" — concretely:** Tiers B and C shell out to `docker run`/`podman run --device <our spec>` and assert that **a tool which exists only in the dev-shell (not the base image) executes inside the container**, and that the container's `PATH` contains the dev-shell prefix. That is the propagation check.

**Test-env assumptions:** Go toolchain; at least one CDI-capable runtime for Tiers B/C (else skip); `nix`+`direnv` only for Tier C; **no jq** anywhere.

---

## 6. Milestones

1. **Scaffold** — Go module, CLI skeleton, CI (build + Tier A), `flake.nix`.
2. **`gen` (shared mode)** — devshell discovery + closure walk + spec build/validate + write + `$DIRENV_CDI`. Tier A tests.
3. **`hook`** — port the wrap logic with typed OCI spec; Tier A tests against synthetic `config.json`.
4. **Tier B integration** — runtime detection + the 13 assertions; **podman first**, then add the docker path.
5. **`gen --mode local`** — and cover both placements in Tier B.
6. **Tier C smoke** — real-flake fixture, nix-gated.
7. **direnv integration + docs** — `direnvrc` snippet (`nix-direnv-cdi gen` after `use flake`, `eval $(... )` to export `$DIRENV_CDI`), one-time registration (`install`), README with the `--device "$DIRENV_CDI"` and compose `deploy.resources.reservations.devices` recipes.
8. **Packaging** — nix flake app for `nix run` / profile install; the hook binary path the spec references resolves to the installed binary.

---

## 7. Non-goals & notes

- **Non-goals:** the OCI runtime-shim approach (too heavy); the FHS-tail `PATH` override (rejected — we do true additive via the hook); prefixes that are *not* host-accessible bind mounts.
- **Known limitation:** absolute path into a RO-mounted prefix (T9) — runs, not additive; documented, not fixed (run tools by name).
- **General, not nix-only:** the mechanism works for any bind-mounted, host-accessible prefix; the nix-direnv name reflects the primary use, not a hard dependency.
- **Source of truth for behaviour:** `../cdi-additive-test.sh` (13/13). Keep it in the repo as the executable reference; Tier B is its Go-driven successor.
