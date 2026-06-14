# Architecture

nix-direnv-cdi makes a project's **nix-direnv dev-shell** usable inside any OCI
container via **one generic CDI device**. The device carries no project data —
only a `createRuntime` hook. At container-creation time that hook injects the
project's dev-shell *dynamically*, reading what it needs from the loaded direnv
environment it inherits.

The result: a **single** registered device (`nix-direnv.cdi/shell=devshell`)
serves every project on the machine. No per-project specs, no fingerprints,
nothing baked.

## Two phases

| Phase | When | What | Where |
|-------|------|------|-------|
| **Generate** | in your project (e.g. `.envrc`) | compute the dev-shell's store **closure** | `.direnv/cdi/mounts.json` |
| **Inject** | at `podman run --device …` | bind-mount the closure into the container + make `PATH`/env additive | the running container |

The slow, project-specific part (the closure) is produced ahead of time by
`gen`; the dynamic part (which closure, what `PATH`/env) is resolved by the hook
at run time from the live environment. See [data-flow.md](data-flow.md).

## Components

```
main.go                 # subcommand dispatch: gen | hook | install | version
internal/
  cdispec/              # build + validate + write the single generic device (CNCF libs)
  devshell/             # closure (gcroot -> nix-store -qR); decode DIRENV_DIFF (prefix+env);
                        #   read/write .direnv/cdi/mounts.json
  hook/                 # the createRuntime hook: gate -> mount-inject -> wrap entrypoint
  nsmount/              # enter the container's mount ns and bind-mount the closure
  ociconfig/            # read OCI State (stdin) + the bundle's config.json
  install/              # register the generic device dir with podman/docker
flake.nix               # nix run / profile install; version-stamped static binary
contrib/use_cdi.sh      # optional direnvrc `use cdi` helper
```

## Subcommands

- **`install`** — write the generic device to `~/.config/cdi/nix-direnv.json`
  (hook `path` = the installed binary) and register that directory with podman
  (`containers.conf.d` drop-in) and docker (`daemon.json`) — backing up any
  existing config first and printing the manual steps if it can't apply them
  (e.g. docker's root-owned `daemon.json`). One-time per machine.
- **`gen`** — resolve the gcroot under `.direnv/flake-profile-*`, walk the
  closure (`nix-store -qR`), write `.direnv/cdi/mounts.json`, and report the
  constant device reference. Needs no `DIRENV_DIFF`, so it runs inside
  `.envrc` right after `use flake`.
- **`hook`** — the `createRuntime` hook (invoked by the runtime, not by you).
  Gates on `DIRENV_DIR`, injects the closure via `nsmount`, and wraps the
  entrypoint for additive `PATH` + dev-shell env decoded from `DIRENV_DIFF`.
  Best-effort: always exits 0.
- **`version`**.

## Artifacts

| Artifact | Produced by | Contents |
|----------|-------------|----------|
| `~/.config/cdi/nix-direnv.json` | `install` | the one generic device (hook only) |
| `<project>/.direnv/cdi/mounts.json` | `gen` | `{"closure": ["/nix/store/…", …]}` |

## See also

- [mechanisms.md](mechanisms.md) — how the hook actually injects mounts and PATH.
- [data-flow.md](data-flow.md) — the end-to-end timeline.
- [design-decisions.md](design-decisions.md) — why it's shaped this way.
