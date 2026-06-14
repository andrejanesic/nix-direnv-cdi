# How it works

How the dev-shell actually reaches inside the container, end to end. Three
mechanisms stack — the **CDI device + hook**, **dynamic mount injection** (the
closure), and **additive `PATH`** (the entrypoint wrapper) — and the second half
of this doc walks the whole timeline, from `install` to a running container, and
shows what data is read when. The low-level kernel/Go tricks each mechanism
relies on are in [internals.md](internals.md).

## 1. The CDI device and the createRuntime hook

A **CDI** (Container Device Interface) spec is a static JSON file describing a
"device". When you `podman run --device nix-direnv-cdi.org/env=current …`, the
runtime frontend injects that device's `containerEdits` into the OCI
`config.json`. Our device's only edit is a single hook:

```json
{ "hookName": "createRuntime", "path": "<installed binary>", "args": ["nix-direnv-cdi","hook"] }
```

Because it's a **standard OCI hook embedded in the CDI device**, it runs under
both **crun** (podman) and **runc** (docker) — the device is cross-runtime, and
opt-in (it only runs on containers you attach it to).

Daemon-driven CLIs such as Docker may not pass the client shell environment to
the hook. For Docker, pass `DIRENV_DIR` and `DIRENV_DIFF` through to the OCI
process env (`--env DIRENV_DIR --env DIRENV_DIFF`). The hook uses that as a
fallback when the hook environment lacks direnv context.

`createRuntime` is the chosen lifecycle stage because it is the one that runs **in
the host (runtime) namespace, after the container's mount namespace and mounts
exist, but before `pivot_root`** — so it can read `config.json` and the OCI
`State` (which carries the container `pid`) *and* affect the container's rootfs.

## 2. Dynamic mount injection (the closure)

The dev-shell's tools live in `/nix/store`. The container must see those store
paths. We do **not** list them in the CDI spec (that would make the device
project-specific); instead the hook bind-mounts them at run time.

**Why not just mount host-side?** The hook runs in the host mount namespace, but
the container has its own mount namespace with private/slave propagation — a
bind mount the hook makes host-side under the rootfs does **not** appear in the
container. (File *writes* into the rootfs do propagate; mounts do not.)

**What works:** the hook **enters the container's mount namespace** (via the
`pid` from the OCI State) and bind-mounts there:

1. read `<project>/.direnv/cdi/mounts.json` (the closure, located via the
   inherited `DIRENV_DIR`);
2. `unshare(CLONE_FS)` then `setns(CLONE_NEWNS)` into the container's mount ns
   (on a dedicated, discarded OS thread — see [internals.md](internals.md));
3. for each closure path, bind it onto `<rootfs>/<path>` (read-only,
   best-effort).

Source paths (`/nix/store/X`) are reachable from inside the container's mount ns
*before* `pivot_root`, and mounts created under `root.path` survive `pivot_root`,
appearing at `/nix/store/X`. This is the same technique NVIDIA's
`libnvidia-container` uses to inject GPU mounts.

## 3. Additive `PATH` (the entrypoint wrapper)

The dev-shell needs to be *prepended* to the image's `PATH`, not replace it. But:

- CDI `env` is **set-only** — it can write `PATH=…` but can't append to the
  image's real `PATH`.
- OCI hooks **cannot modify `process.env`/`process.args`**.

So additive `PATH` can't be expressed as data. The hook instead **wraps the
entrypoint**: it writes a shim that prepends the prefix and exports the dev-shell
env, then execs the real entrypoint.

```sh
#!/bin/sh
unset DIRENV_DIR DIRENV_DIFF
export PATH="<nix prefix>:$PATH"
export CC='gcc'        # dev-shell env vars, single-quoted
exec "<real entrypoint>" "$@"
```

The prefix and env come from decoding **`DIRENV_DIFF`** at run time (not baked
into the spec). Resolution mirrors `command -v`:

- **relative** entrypoint → resolve across `prefix` (host-accessible `/nix`
  paths) then the image `PATH`; drop the shim into the first image-`PATH` dir so
  the runtime finds it first.
- **absolute** entrypoint in the writable rootfs → wrap in place (move the real
  aside).
- **absolute path into the read-only store** → left intact (the **T9**
  limitation; see [limitations.md](limitations.md)).

### Behaviour matrix (T1–T10)

The additive-`PATH` contract is pinned by a numbered matrix; the test suite
asserts against these IDs (grep `T1`…`T10` in `*_test.go`).

| ID | Behaviour |
|----|-----------|
| T1 | entrypoint = shell → `PATH` additive (prefix prepended, image base preserved) |
| T2 | a dev-shell-only tool is reachable via the additive `PATH` |
| T3 | base-image tools still resolve |
| T4 | the wrapped entrypoint execs the real binary |
| T5 | works for `sh` — no shebang recursion |
| T6 | control: **without** the device, `PATH` is the plain image default |
| T7 | a dev-shell-only tool as the **bare** entrypoint runs |
| T8 | additive `PATH` holds even when the entry is a dev-shell-only tool |
| T9 | limitation: absolute path into a read-only mount runs but is **not** additive |
| T10 | absolute path into the **writable** rootfs **is** wrapped |

## End-to-end timeline

Three moments matter, and keeping them separate is the key to the whole design:
**setup** (once), **generate** (per project, gen-time), and **inject** (per
`podman run`, run-time).

### Setup — once per machine

```
nix-direnv-cdi install
  └─ writes ~/.config/cdi/nix-direnv.json   (the one generic device; hook path = installed binary)
  └─ registers that dir with podman (containers.conf.d drop-in) and docker (daemon.json)
```

### Generate — per project, in `.envrc`

```
use flake                       # nix-direnv materialises .direnv/flake-profile-* (the gcroot)
nix-direnv-cdi gen
  ├─ gcroot ──nix-store -qR──▶ closure  ──▶ .direnv/cdi/mounts.json   {"closure":[…]}
  └─ device ref to attach (constant): nix-direnv-cdi.org/env=current
```

`gen` needs only the **gcroot**, not `DIRENV_DIFF` — which is why it can run
*during* `.envrc` evaluation, where `DIRENV_DIFF` is not yet set (see
[internals.md](internals.md)). That timing is what makes the `use cdi`
integration possible.

### Inject — per container run

```
$ podman run --device nix-direnv-cdi.org/env=current busybox hello      # from the loaded dev-shell

TIME A  podman frontend
  loads ~/.config/cdi/nix-direnv.json → injects the createRuntime hook into config.json

TIME B  crun creates the container
  ┌─ namespaces created; rootfs + mounts performed ────────────────────────┐
  │  ► createRuntime hook fires (host ns, container pid known, pre-pivot)   │
  │      reads:  OCI State (stdin) ........... pid, bundle                  │
  │             config.json ................. rootfs, process.args, PATH    │
  │             inherited env ............... DIRENV_DIR, DIRENV_DIFF       │
  │             <DIRENV_DIR>/.direnv/cdi/mounts.json ... the closure        │
  │      gate:  DIRENV_DIR present?  no → exit 0 (inert)                    │
  │      mount: setns into the container mount ns; bind each closure path   │
  │             (mechanism 2)                                               │
  │      wrap:  decode DIRENV_DIFF → prefix+env; shim the entrypoint        │
  │             (mechanism 3)                                               │
  │  pivot_root                                                            │
  └─────────────────────────────────────────────────────────────────────────┘

TIME C  the (wrapped) entrypoint execs
  PATH = <nix prefix>:<image PATH>;  dev-shell tools resolve from the mounted closure
  → "Hello, world!"
```

## What lives where

| Data | Source | Read at | By |
|------|--------|---------|----|
| closure (`/nix/store` paths) | gcroot → `nix-store -qR` | gen-time | `gen` → `mounts.json` |
| which device | constant `nix-direnv-cdi.org/env=current` | — | the `--device` arg |
| project root / gate | `DIRENV_DIR` (hook env or OCI process env fallback) | run-time | hook |
| `PATH` prefix + dev-shell env | `DIRENV_DIFF` (hook env or OCI process env fallback) | run-time | hook |
| container pid / rootfs | OCI State (stdin) / `config.json` | run-time | hook |

The split is deliberate: the **closure** is captured once at gen-time (it changes
only with dependencies), while **`PATH`/env** are read live at run-time — always
fresh, and never written to disk (see [security.md](security.md)).

## Why one device serves every project

Because the closure comes from the gcroot and `PATH`/env come from the live
`DIRENV_DIFF`, the only thing the *device* identifies is "the current
environment" — not *which* project, closure, or secret. The launching shell
decides which project, at run time, via the inherited environment. That is why
**one** generic device serves every project.
For the reasoning behind each of these choices, see
[design-decisions.md](design-decisions.md).
