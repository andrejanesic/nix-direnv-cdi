# AGENTS.md

Orientation for AI agents (and new contributors) working in this repo. For full
documentation, start at **[docs/readme.md](docs/readme.md)**.

## What this project is

`nix-direnv-cdi` makes a project's **nix-direnv dev-shell** available inside any
OCI container (docker, podman) via **one generic CDI device**. The device holds
no project data — only a `createRuntime` hook. At `podman run --device …` the
hook, which inherits the loaded direnv environment, **bind-mounts the
dev-shell's `/nix/store` closure into the container** (by entering its mount
namespace) and **wraps the entrypoint** for additive `PATH` + dev-shell env. One
device serves every project; the launching shell decides which dev-shell at
run time.

## Repo map

| Path | Role |
|------|------|
| `main.go` | subcommand dispatch |
| `internal/cdispec/` | build/validate/write the single generic CDI device |
| `internal/devshell/` | closure (gcroot → `nix-store -qR`); decode `DIRENV_DIFF`; `mounts.json` I/O |
| `internal/hook/` | the `createRuntime` hook: gate → mount-inject → wrap entrypoint |
| `internal/nsmount/` | enter the container mount ns and bind-mount the closure |
| `internal/ociconfig/` | read OCI State (stdin) + `config.json` |
| `internal/install/` | register the device dir with podman/docker (backup-then-auto) |
| `integration/` | synthetic and e2e integration tests plus the flake fixture |
| `contrib/use_cdi.sh` | optional `use cdi` direnvrc helper (runs `gen` inside `.envrc`) |
| `flake.nix` | `nix run` / profile install; version-stamped static binary |
| `docs/` | reference documentation (see `docs/readme.md`) |

## Build & test

```sh
gofmt -w .  # Format code
go build ./...
go test ./... -skip '^(TestSynthetic|TestE2E)'  # unit tests only
go test ./...                                   # unit + integration tests
NDC_CONTAINER_CLI=podman go test ./...          # unit + integration tests on Podman
nix build .#nix-direnv-cdi  # package the binary
```

## Engineering & troubleshooting

The full documentation is available in `docs/`; for a ToC, see
[docs/readme.md](docs/readme.md). Refer to the docs if you are:

- Analyzing the project before implementing a task
- Stuck designing a solution
- Cannot solve a technical problem or overcome environment constraints
- Have to make a decision that goes against important design principles
  established so far in the project

## Contributing guidelines

Before starting a task or before opening a PR, read
[CONTRIBUTING.md](./CONTRIBUTING.md) and ensure you comply with all requirements.