# Changelog

All notable user-facing changes are recorded here.

This project follows SemVer for tagged releases. Release notes should call out
runtime support changes, installer behavior changes, security-relevant changes,
and known issues explicitly.

## v0.1.0

### Added

- Release and distribution policy covering versioning, artifacts, verification,
  upgrades, and rollback.

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
  `govulncheck`, CodeQL, and Dependabot added to CI.

