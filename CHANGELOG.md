# Changelog

All notable user-facing changes are recorded here.

This project follows SemVer for tagged releases. Release notes should call out
runtime support changes, installer behavior changes, security-relevant changes,
and known issues explicitly.

## Unreleased

### Fixed

- **Rootless podman: hook no longer goes inert.** Rootless podman reports
  `bundle="/"` in the createRuntime State, so the hook's `<bundle>/config.json`
  read resolved to a non-existent `/config.json` and the device did nothing
  (`read /config.json: no such file or directory`). The hook now (1) takes the
  rootfs from the State's `root` field (set even when `bundle` is unusable) and
  (2) falls back to podman's rootless config path
  (`<graphroot>/overlay-containers/<id>/userdata/config.json`) when
  `<bundle>/config.json` is unreadable. If `config.json` remains unreachable the
  hook degrades to mount-only instead of becoming inert. Local podman tests
  missed this because rootful podman passes a correct `bundle`.

## v0.1.0

First public release. `nix-direnv-cdi` exposes a project's nix-direnv dev-shell
inside any OCI container (podman, docker) through a single generic CDI device.
The device carries no project data — only a `createRuntime` hook that, at
`podman run --device …`, bind-mounts the dev-shell's `/nix/store` closure into
the container and wraps the entrypoint for an additive `PATH` and dev-shell
environment. One device serves every project; the launching shell decides which
dev-shell at run time.

### Added

- **Generic CDI device model.** A single, project-independent device
  (`nix-direnv-cdi.org/env=current`) works for every project — no per-project
  fingerprint or regeneration of the device spec.
- **`gen`.** Computes the dev-shell closure from the direnv gcroot
  (`nix-store -qR`) and writes it to `<project>/.direnv/cdi/mounts.json`, the
  data the runtime hook bind-mounts. Safe to run inside `.envrc` right after
  `use flake`.
- **`hook`.** The `createRuntime` hook: gates on the loaded direnv environment,
  enters the container's mount namespace to inject the closure as read-only bind
  mounts (rootless, via `setns` in a child process), then wraps the entrypoint
  for additive `PATH` + dev-shell env. Best-effort by contract — it never breaks
  the container, even on panic.
- **`install` / `uninstall`.** One-time registration of the generic device dir
  with podman and docker (backup-then-auto), and clean rollback.
- **`version`.** Reports version, commit, and build date.
- **`use cdi` direnvrc helper** (`contrib/use_cdi.sh`) that runs `gen` from
  within `.envrc`.
- **Nix packaging.** `nix run` / `nix profile install` of a version-stamped
  static binary via `flake.nix`; declarative install via `environment.etc` /
  `xdg.configFile` on NixOS.
- **Documentation** under [docs/](docs/readme.md): usage guide, architecture,
  mechanisms, design decisions, security model, limitations and runtime support
  matrix, and the release process.
- **Worked example** ([example/](example/readme.md)): a coding agent running in
  a container via the device.
- **Integration tests:** a nix-free synthetic tier and a nix-gated real-flake
  end-to-end tier, runnable against rootless or rootful podman.
- Release and distribution policy covering versioning, artifacts, verification,
  upgrades, and rollback.

### Runtime support

- Rootless and rootful podman, and docker. Bare rootless `runc` (no higher-level
  runtime) is explicitly out of scope. See
  [docs/limitations.md](docs/limitations.md) for the full matrix.

### Security

- Closure paths in `mounts.json` are validated before mounting: each must be a
  clean, absolute path under `/nix/store` (or `$NIX_STORE_DIR`), so a tampered
  `mounts.json` cannot bind-mount an arbitrary host path into the container.
- Dev-shell environment variable names are validated before being written into
  the wrapped entrypoint, closing a shell-injection vector via a crafted name.
- The inflated size of `DIRENV_DIFF` is capped to guard against a decompression
  bomb.
- The hook debug log (`NDC_HOOK_LOG`) is created `0600` and no longer follows
  symlinks; point it at a private path.
- `install`/`uninstall` no longer follow symlinks when writing specs, drop-ins,
  or backups.
- Documented the threat model and shared-host guidance in
  [docs/security.md](docs/security.md).

### Changed

- Supply chain hardened: all GitHub Actions pinned to commit SHAs, with
  `govulncheck`, CodeQL, and Renovate added to CI.

