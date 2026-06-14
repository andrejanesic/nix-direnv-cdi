# nix-direnv-cdi

**Your project's nix dev-shell — inside any container, with one flag.**

You already have a perfect, reproducible toolchain in your `flake.nix`: the right
Go, Node, compilers, linters, CLIs, pinned to the bit. nix-direnv-cdi teleports
that dev-shell **into any OCI container** — no Dockerfile, no `apt-get`, no
rebuilding images — by attaching a single CDI device:

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

**1. Install once per machine** (writes + registers the one generic device):

```sh
nix run github:andrejanesic/nix-direnv-cdi -- install
# or: nix profile install github:andrejanesic/nix-direnv-cdi && nix-direnv-cdi install
```

If Docker is configured, restart it after `install` so any
`/etc/docker/daemon.json` change is loaded:

```sh
sudo systemctl restart docker
```

Minimal smoke test:

```sh
podman run --rm --device nix-direnv-cdi.org/env=current busybox true
```

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
  Dev-shell env (incl. secrets) is read live and **never written to disk**.
- **Runtimes.** Verified end-to-end on **podman** and **docker**. See
  [docs/limitations.md](docs/limitations.md).
- **Docker daemon env.** Docker runs containers through a daemon, so pass
  `DIRENV_DIR` and `DIRENV_DIFF` through (`--env DIRENV_DIR --env DIRENV_DIFF`,
  or the Compose `environment` keys above) when using Docker directly.
- **Limitation (T9).** An absolute path *into* the read-only store runs but isn't
  made additive — run dev-shell tools by name.

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
- this tool's shared CDI dir entry in Docker's `/etc/docker/daemon.json`
  `cdi-spec-dirs`

Run this to remove those owned entries. It removes the spec file, podman
drop-in, and Docker registration entry; it leaves the shared directory itself in
place.

```sh
nix-direnv-cdi uninstall
```

If Docker's `daemon.json` changed, restart Docker after uninstall:

```sh
sudo systemctl restart docker
```

Manual rollback is the same set of conservative edits:

1. Delete only the generic CDI spec file:
   `rm ~/.config/cdi/nix-direnv.json` (or the matching `$XDG_CONFIG_HOME/cdi`
   path).
2. For podman, delete only this owned drop-in:
   `~/.config/containers/containers.conf.d/nix-direnv-cdi.conf` (or the matching
   `$XDG_CONFIG_HOME/containers/...` path). Leave other podman config and other
   drop-ins in place.
3. For Docker, edit `/etc/docker/daemon.json` and remove only this tool's shared
   CDI dir from `cdi-spec-dirs`. Preserve unrelated Docker settings and other
   CDI directories. If that entry was the only CDI dir, remove the
   `cdi-spec-dirs` key instead of leaving an empty array. Restart Docker after
   saving the file.

Before rewriting existing config, the installer writes a same-directory backup
named `<path>.bak` when it can, for example `/etc/docker/daemon.json.bak` or
`~/.config/containers/containers.conf.d/nix-direnv-cdi.conf.bak`. Uninstall also
backs up Docker's `daemon.json` before rewriting it; the owned podman drop-in is
removed directly. To recover, compare the backup with the current file, then
restore the needed file contents and restart Docker if `/etc/docker/daemon.json`
was restored.

## Documentation

- **[docs/](docs/readme.md)** — architecture, mechanisms (incl. data flow),
  design decisions, security, limitations, internals.
- **[CONTRIBUTING.md](CONTRIBUTING.md)** — build, test, and integration
  validation commands.
- **[AGENTS.md](AGENTS.md)** — orientation for AI agents working in this repo.

## License

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE).

## Disclaimer

nix-direnv-cdi is an independent project and is not affiliated with or endorsed
by the NixOS Foundation, the Nix project, or the direnv project.
