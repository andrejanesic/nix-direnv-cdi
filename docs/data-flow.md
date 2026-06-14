# Data flow

Three moments matter, and keeping them separate is the key to the whole design:
**setup** (once), **generate** (per project, gen-time), and **inject** (per
`podman run`, run-time).

## Setup — once per machine

```
nix-direnv-cdi install
  └─ writes ~/.config/cdi/nix-direnv.json   (the one generic device; hook path = installed binary)
  └─ registers that dir with podman (containers.conf.d drop-in) and docker (daemon.json)
```

## Generate — per project, in `.envrc`

```
use flake                       # nix-direnv materialises .direnv/flake-profile-* (the gcroot)
eval "$(nix-direnv-cdi gen)"
  ├─ gcroot ──nix-store -qR──▶ closure  ──▶ .direnv/cdi/mounts.json   {"closure":[…]}
  └─ prints: export DIRENV_CDI=nix-direnv.cdi/shell=devshell
```

`gen` needs only the **gcroot**, not `DIRENV_DIFF` — which is why it can run
*during* `.envrc` evaluation (where `DIRENV_DIFF` is not yet set). See
[gotchas.md](gotchas.md).

## Inject — per `podman run`, three sub-times

```
$ podman run --device "$DIRENV_CDI" busybox hello      # from the loaded dev-shell

TIME A  podman frontend
  loads ~/.config/cdi/nix-direnv.json → injects the createRuntime hook into config.json

TIME B  crun creates the container
  ┌─ namespaces created; rootfs + mounts performed ────────────────────────┐
  │  ► createRuntime hook fires (host ns, container pid known, pre-pivot)   │
  │      reads:  OCI State (stdin) ........... pid, bundle                  │
  │             config.json ................. rootfs, process.args, PATH   │
  │             inherited env ............... DIRENV_DIR, DIRENV_DIFF       │
  │             <DIRENV_DIR>/.direnv/cdi/mounts.json ... the closure        │
  │      gate:  DIRENV_DIR present?  no → exit 0 (inert)                    │
  │      mount: setns into the container mount ns; bind each closure path   │
  │      wrap:  decode DIRENV_DIFF → prefix+env; shim the entrypoint        │
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
| which device | constant `nix-direnv.cdi/shell=devshell` | — | `$DIRENV_CDI` |
| project root / gate | `DIRENV_DIR` (inherited) | run-time | hook |
| `PATH` prefix + dev-shell env | `DIRENV_DIFF` (inherited) | run-time | hook |
| container pid / rootfs | OCI State (stdin) / `config.json` | run-time | hook |

The split is deliberate: the **closure** is captured once at gen-time (it changes
only with dependencies), while **`PATH`/env** are read live at run-time (always
fresh, never written to disk — see [security.md](security.md)).

## Key property

Because the closure comes from the gcroot and `PATH`/env come from the live
`DIRENV_DIFF`, the only thing the *device* identifies is "a dev-shell" — not
*which* one. The launching shell decides which project, at run time, via the
inherited environment. That is why **one** device serves every project.
