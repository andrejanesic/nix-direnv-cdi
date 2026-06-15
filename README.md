# nix-direnv-cdi

**Your project's nix dev-shell — inside supported containers, with one flag.**

You already have a perfect, reproducible toolchain in your `flake.nix`: the right
Go, Node, compilers, linters, CLIs, pinned to the bit. nix-direnv-cdi teleports
that dev-shell **into containers run by CDI-capable OCI runtimes** — no
Dockerfile, no `apt-get`, no rebuilding images — by attaching a single CDI
device:

```sh
podman run --device nix-direnv-cdi.org/env=current <any-image> <your-tool>
```

One generic device serves every project. The right dev-shell is chosen
automatically, at run time, from the direnv environment you're already in.

---

## See it in action

**The proof — a tool that exists only in your dev-shell, running in a stock image:**

```console
$ podman run --device nix-direnv-cdi.org/env=current busybox hello
Hello, world!          # ← `hello` came from your dev-shell, not from busybox
```

**Run your real toolchain in a minimal image** — `go` here comes from the
dev-shell; `alpine` doesn't ship it:

```sh
podman run --device nix-direnv-cdi.org/env=current -v "$PWD:$PWD" -w "$PWD" alpine \
  go test ./...
```

**Reproducible CI / "works on my machine", gone** — the container runs the exact
same tools as your laptop, with no custom image to build or maintain.

**docker compose** — give a service your dev-shell:

```yaml
services:
  dev:
    image: alpine
    environment:
      - DIRENV_DIR
      - DIRENV_DIFF
    command: go build ./...
    deploy:
      resources:
        reservations:
          devices:
            - driver: cdi
              device_ids: ["nix-direnv-cdi.org/env=current"]
```

---

## Quick start

Prerequisites:

- Nix, direnv, and nix-direnv
- flakes enabled if your project uses `use flake`
- podman, or Docker Engine with CDI support

**1. Install once per machine** (writes + registers the one generic device):

```sh
nix run github:andrejanesic/nix-direnv-cdi -- install
# or: nix profile install github:andrejanesic/nix-direnv-cdi && nix-direnv-cdi install
```

For a released version, pin the tag:

```sh
nix profile install github:andrejanesic/nix-direnv-cdi/v0.1.0
nix-direnv-cdi install
nix-direnv-cdi version
```

For Docker, `install` uses the daemon-scanned system CDI spec path
`/etc/cdi/nix-direnv.json`. Docker is system-wide, so the installer does not add
your per-user `~/.config/cdi` directory to `/etc/docker/daemon.json`.
If that write needs privileges, the command prints the exact manual fallback:
install the generated spec from `~/.config/cdi/nix-direnv.json` (or the matching
`$XDG_CONFIG_HOME/cdi` path) to `/etc/cdi/nix-direnv.json`, for example with
`sudo install -D -m 0644`. Per-runtime registration errors print manual fallback
commands instead of making the whole install fail.

Minimal post-install smoke test:

```sh
podman run --rm --device nix-direnv-cdi.org/env=current busybox true
```

Expected result: no output and exit status 0. This proves the generic device
resolves and the inert hook does not break a plain container.

**2. In your project's `.envrc`:**

```sh
use flake
nix-direnv-cdi gen               # writes .direnv/cdi/mounts.json
```

(or copy [`contrib/use_cdi.sh`](contrib/use_cdi.sh) into `~/.config/direnv/direnvrc`
and just write `use flake` then `use cdi`.)

The device reference is always the constant `nix-direnv-cdi.org/env=current`.

**3. Run anything with your dev-shell attached:**

```sh
podman run --device nix-direnv-cdi.org/env=current <image> <cmd>
```

For Docker, pass direnv's bookkeeping variables through to the OCI process env:

```sh
docker run --env DIRENV_DIR --env DIRENV_DIFF \
  --device nix-direnv-cdi.org/env=current <image> <cmd>
```

End-to-end smoke test from inside a loaded dev-shell:

```console
$ podman run --rm --device nix-direnv-cdi.org/env=current busybox hello
Hello, world!
```

Use a command that exists in your dev-shell but not in the image. The `hello`
example assumes your dev-shell includes GNU hello, as the integration fixture
does.

---

## How it works (in one breath)

`install` registers **one** generic CDI device whose only content is a
`createRuntime` hook. `gen` records your dev-shell's `/nix/store` **closure** to
`.direnv/cdi/mounts.json`. When you `podman run --device …` *from the loaded
dev-shell*, the hook — which inherits your direnv environment — **bind-mounts
that closure into the container** (by entering its mount namespace) and **wraps
the entrypoint** so the dev-shell's `bin` dirs are prepended to `PATH` and its
env vars are set. Nothing per-project is baked into the device; the launching
shell decides which dev-shell, at run time.

→ Full design, mechanisms, and data flow: **[docs/](docs/readme.md)**.

## Good to know

- **Being in the dev-shell is the authorization.** The device only does anything
  when launched from a shell that has the project's dev-shell loaded (you ran
  `direnv allow`). Anywhere else it's **inert** — it mounts nothing. See
  [docs/security.md](docs/security.md).
- **Surgical, not the whole store.** Only this project's closure is mounted,
  read-only (best-effort under rootless; nix store paths are immutable anyway).
  Dev-shell env (incl. secrets) is read live and **never written to disk**. When
  Docker passthrough is used, the wrapper unsets `DIRENV_DIR` and `DIRENV_DIFF`
  before execing the real entrypoint.
- **Runtimes.** Verified end-to-end on **podman** and **docker**. See
  [docs/limitations.md](docs/limitations.md). Release validation runs the same
  e2e suite with `NDC_CONTAINER_CLI=docker` and `NDC_CONTAINER_CLI=podman`.
- **Docker daemon env.** Docker runs containers through a daemon, so pass
  `DIRENV_DIR` and `DIRENV_DIFF` through (`--env DIRENV_DIR --env DIRENV_DIFF`,
  or the Compose `environment` keys above) when using Docker directly.
- **Limitation (T9).** An absolute path *into* the read-only store runs but isn't
  made additive — run dev-shell tools by name.

## If nothing happens

If the container runs but the dev-shell is missing:

- Confirm you launched from the loaded dev-shell: `echo "$DIRENV_DIR"` should be
  non-empty.
- Re-run `nix-direnv-cdi gen` or reload direnv so `.direnv/cdi/mounts.json` is
  current.
- Confirm you passed `--device nix-direnv-cdi.org/env=current`.
- For Docker, add `--env DIRENV_DIR --env DIRENV_DIFF`.
- For `sudo`, preserve env with `sudo -E` or
  `sudo --preserve-env=DIRENV_DIR,DIRENV_DIFF`.
- For hook tracing, set `NDC_HOOK_LOG=/tmp/ndc-hook.log` before the container
  command.

Full troubleshooting: [docs/limitations.md](docs/limitations.md).

## Uninstall and manual rollback

`nix-direnv-cdi install` owns only machine-level registration for the generic
device:

- the shared CDI spec directory: `$XDG_CONFIG_HOME/cdi`, or `~/.config/cdi`
  when `XDG_CONFIG_HOME` is unset
- the shared CDI spec file: `$XDG_CONFIG_HOME/cdi/nix-direnv.json`, or
  `~/.config/cdi/nix-direnv.json` when `XDG_CONFIG_HOME` is unset
- the podman drop-in:
  `$XDG_CONFIG_HOME/containers/containers.conf.d/nix-direnv-cdi.conf`, or
  `~/.config/containers/containers.conf.d/nix-direnv-cdi.conf`
- the Docker system CDI spec: `/etc/cdi/nix-direnv.json`

Run this to remove those owned entries. It removes the spec file, podman
drop-in, and Docker system CDI spec; it leaves the shared directory itself in
place.

```sh
nix-direnv-cdi uninstall
```

Manual rollback is the same set of conservative edits:

1. Delete only the generic CDI spec file:
   `rm ~/.config/cdi/nix-direnv.json` (or the matching `$XDG_CONFIG_HOME/cdi`
   path).
2. For podman, delete only this owned drop-in:
   `~/.config/containers/containers.conf.d/nix-direnv-cdi.conf` (or the matching
   `$XDG_CONFIG_HOME/containers/...` path). Leave other podman config and other
   drop-ins in place.
3. For Docker, remove only this tool-owned system CDI spec:
   `sudo rm /etc/cdi/nix-direnv.json`. Do not remove other CDI specs in
   `/etc/cdi`.

Backup behavior is limited and predictable:

- `install` creates a same-directory `<path>.bak` only before rewriting an
  existing file with different content, for example
  `/etc/cdi/nix-direnv.json.bak` or
  `~/.config/containers/containers.conf.d/nix-direnv-cdi.conf.bak`.
- If the existing file already matches what `install` would write, it is a
  no-op and no backup is created or overwritten.
- If a `.bak` already exists and a real rewrite is needed, the backup path is
  overwritten with the pre-rewrite content.
- `uninstall` does not create backups. It removes only the owned files listed
  above and reports no-op when they are already absent.

Docker `daemon.json` is only relevant for an advanced manual custom CDI
directory setup; changing that file may require a Docker restart.

Package rollback is separate from unregistering the CDI device. For Nix
profiles, use Nix profile generations or reinstall a pinned release, then rerun
`nix-direnv-cdi install` so the CDI spec points at the active binary. See
[docs/release.md](docs/release.md) for release install, verification, upgrade,
and rollback commands.

## Documentation

- **[docs/](docs/readme.md)** — architecture, mechanisms (incl. data flow),
  design decisions, security, limitations, internals.
- **[docs/release.md](docs/release.md)** — release channels, artifact
  verification, upgrade, rollback, and maintainer checklist.
- **[CHANGELOG.md](CHANGELOG.md)** — release notes.
- **[CONTRIBUTING.md](CONTRIBUTING.md)** — build, test, and integration
  validation commands.
- **[AGENTS.md](AGENTS.md)** — orientation for AI agents working in this repo.

## License

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE).

## Disclaimer

nix-direnv-cdi is an independent project and is not affiliated with or endorsed
by the NixOS Foundation, the Nix project, or the direnv project.
