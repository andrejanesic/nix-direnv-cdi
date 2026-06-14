# AGENTS.md

Orientation for AI agents (and new contributors) working in this repo. For full
documentation, start at **[docs/readme.md](docs/readme.md)**.

## What this project is

`nix-direnv-cdi` makes a project's **nix-direnv dev-shell** available inside any
OCI container (podman, docker) via **one generic CDI device**. The device holds
no project data — only a `createRuntime` hook. At `podman run --device …` the
hook, which inherits the loaded direnv environment, **bind-mounts the dev-shell's
`/nix/store` closure into the container** (by entering its mount namespace) and
**wraps the entrypoint** for additive `PATH` + dev-shell env. One device serves
every project; the launching shell decides which dev-shell at run time.

A single Go binary, no runtime deps. CLI: `gen | hook | install | version`.

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
| `flake.nix` | `nix run` / profile install; version-stamped static binary |
| `docs/` | reference documentation (see `docs/readme.md`) |

## Build & test

```sh
go build ./...
go test ./... -skip '^(TestSynthetic|TestE2E)'  # unit tests only
go test ./...                                   # unit + synthetic/e2e integration for the selected container CLI
nix build .#nix-direnv-cdi  # package the binary
```

Missing integration prerequisites are test failures. Use `-skip` only when
intentionally omitting suites.

## Invariants to respect before editing

These are load-bearing; breaking them silently breaks the tool. Full detail in
[docs/internals.md](docs/internals.md).

- **The hook must always exit 0.** A non-zero `createRuntime` hook aborts the
  container. Everything in `hook`/`nsmount` is best-effort.
- **`nsmount` must `unshare(CLONE_FS)` before `setns(CLONE_NEWNS)`** (Go threads
  share `CLONE_FS` → `EINVAL` otherwise), on a locked-and-discarded thread.
  Mount-ns-only; no `CLONE_NEWUSER` (impossible in pure Go).
- **The gate is `DIRENV_DIR`.** No `DIRENV_DIR` in the hook's env → no-op. This
  is the authorization model, not just a guard.
- **`gen` must not depend on `DIRENV_DIFF`** (it uses the gcroot) so it can run
  inside `.envrc`. The **hook** reads `DIRENV_DIFF` at run time (when it's set).
- **`0755` traversability** for the spec dir, hook binary, and `mounts.json`
  chain under rootless podman.

## Verification expectation

Logic changes to `hook`/`nsmount`/`cdispec` should be re-checked end-to-end
against real container CLIs, not just unit tests — three real bugs in the
dynamic hook were only caught by running a live container (`NDC_HOOK_LOG=<file>`
enables hook tracing). Out of scope: bare rootless `runc` with an unprivileged
invoker (a non-goal).

## Full documentation

→ **[docs/readme.md](docs/readme.md)**
