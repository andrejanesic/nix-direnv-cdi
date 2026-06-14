# Architecture

nix-direnv-cdi makes a project's **nix-direnv dev-shell** usable inside any OCI
container via **one generic CDI device**. The device carries no project data —
only a `createRuntime` hook. At container-creation time that hook injects the
project's dev-shell *dynamically*, reading what it needs from the loaded direnv
environment it inherits.

The result: a **single** registered device (`nix-direnv-cdi.org/env=current`)
serves every project on the machine. No per-project specs, no fingerprints,
nothing baked.

## Two phases

| Phase | When | What | Where |
|-------|------|------|-------|
| **Generate** | in your project (e.g. `.envrc`) | compute the dev-shell's store **closure** | `.direnv/cdi/mounts.json` |
| **Inject** | at `podman run --device …` | bind-mount the closure into the container + make `PATH`/env additive | the running container |

The slow, project-specific part (the closure) is produced ahead of time by
`gen`; the dynamic part (which closure, what `PATH`/env) is resolved by the hook
at run time from the live environment. See [mechanisms.md](mechanisms.md) for the
end-to-end timeline.

## Components

```
main.go                 # subcommand dispatch: gen | hook | install | uninstall | version
internal/
  cdispec/              # build + validate + write the single generic device (CNCF libs)
  devshell/             # closure (gcroot -> nix-store -qR); decode DIRENV_DIFF (prefix+env);
                        #   read/write .direnv/cdi/mounts.json
  hook/                 # the createRuntime hook: gate -> mount-inject -> wrap entrypoint
  nsmount/              # enter the container's mount ns and bind-mount the closure
  ociconfig/            # read OCI State (stdin) + the bundle's config.json
  install/              # register/unregister the generic device dir with podman/docker
flake.nix               # nix run / profile install; version-stamped static binary
contrib/use_cdi.sh      # optional direnvrc `use cdi` helper
```

## Subcommands

- **`install`** — write the generic device to
  `$XDG_CONFIG_HOME/cdi/nix-direnv.json` (or `~/.config/cdi/nix-direnv.json`;
  hook `path` = the installed binary) and register that directory with podman
  (an owned `containers.conf.d` drop-in) and docker (`daemon.json`) — backing up
  any existing config first and printing the manual steps if it can't apply them
  (e.g. docker's root-owned `daemon.json`). Restart Docker after install if
  `/etc/docker/daemon.json` changed. One-time per machine.
- **`uninstall`** — remove only the generic CDI spec
  `$XDG_CONFIG_HOME/cdi/nix-direnv.json` (or `~/.config/cdi/nix-direnv.json`),
  the owned podman drop-in, and this tool's shared CDI dir entry from Docker's
  `cdi-spec-dirs`. It preserves unrelated Docker keys and other CDI dirs,
  removes the Docker key instead of leaving an empty array when this was the
  only CDI dir, backs up Docker config before rewriting, and prints manual
  rollback steps if daemon JSON cannot be edited safely. Restart Docker after
  uninstall if `/etc/docker/daemon.json` changed. See the top-level
  [README](../README.md#uninstall-and-manual-rollback) for manual rollback and
  backup recovery steps.
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
| `$XDG_CONFIG_HOME/cdi` (or `~/.config/cdi`) | `install` | shared CDI spec directory; `uninstall` removes the owned spec file inside it, not the directory itself |
| `$XDG_CONFIG_HOME/cdi/nix-direnv.json` (or `~/.config/cdi/nix-direnv.json`) | `install` | the one generic device (hook only); removed by `uninstall` |
| `$XDG_CONFIG_HOME/containers/containers.conf.d/nix-direnv-cdi.conf` (or `~/.config/containers/...`) | `install` | owned podman drop-in registering the shared CDI dir; only this drop-in should be removed during rollback |
| `/etc/docker/daemon.json` `cdi-spec-dirs` entry for the shared CDI dir | `install` | docker registration; preserve other keys and CDI dirs, remove the key if this was the only CDI dir, and restart Docker after changes |
| `<project>/.direnv/cdi/mounts.json` | `gen` | `{"closure": ["/nix/store/…", …]}` |

## See also

- [mechanisms.md](mechanisms.md) — how the hook injects mounts and PATH, plus
  the end-to-end timeline.
- [design-decisions.md](design-decisions.md) — why it's shaped this way.
