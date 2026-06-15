# Security Policy

## Supported versions

Only the latest release receives security fixes.

| Version | Supported |
|---------|-----------|
| Latest  | ✅        |
| Older   | ❌        |

## Reporting a vulnerability

Use [GitHub Security Advisories](https://github.com/andrejanesic/nix-direnv-cdi/security/advisories/new)
to report vulnerabilities privately. Do not open a public issue for security
concerns.

**Response window:**

- Acknowledgement within **7 days**.
- Resolution or public disclosure within **90 days**, following responsible
  disclosure practices.

## Security model

`nix-direnv-cdi` runs a CDI `createRuntime` hook that enters the container's
mount namespace and bind-mounts a Nix store closure into the container. A few
properties of this model that are relevant to security:

- **Gate is `DIRENV_DIR`.** The hook no-ops when `DIRENV_DIR` is absent from
  the environment it inherits. Exposure requires an active, `direnv allow`-ed
  dev-shell in the launching shell.
- **Opt-in attachment.** The hook runs only on containers that explicitly carry
  the CDI device via `--device`. Unrelated containers are unaffected.
- **Surgical closure only.** Only the project's dev-shell store closure is
  mounted — not the full `/nix/store`.
- **No secrets on disk.** Dev-shell environment variables are decoded from
  `DIRENV_DIFF` at container-creation time and never written to disk.
- **Best-effort, always exits 0.** Hook failures cannot abort or escalate;
  the worst outcome is the dev-shell is not injected.
- **Privilege follows the launcher.** Under rootless Podman the hook runs as
  your mapped subuid inside the user namespace. Under rootful it runs as root.

See [docs/security.md](docs/security.md) for a full treatment of the security
model, privilege boundaries, and known limitations.
