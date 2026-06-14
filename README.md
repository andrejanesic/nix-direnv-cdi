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

If Docker is configured, restart it after `install` so daemon config changes are
loaded:

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
- **Uninstall.** `nix-direnv-cdi uninstall` removes only this tool's owned
  artifacts: `~/.config/cdi/nix-direnv.json`, the podman drop-in
  `~/.config/containers/containers.conf.d/nix-direnv-cdi.conf`, and this shared
  CDI dir from Docker's `/etc/docker/daemon.json`. Restart Docker afterwards if
  that file changed. Manual rollback is the same set of edits: delete the owned
  podman drop-in and generic CDI spec, then remove only the
  `$XDG_CONFIG_HOME/cdi` (or `~/.config/cdi`) entry from Docker's
  `cdi-spec-dirs`, preserving unrelated settings and directories.
- **Limitation (T9).** An absolute path *into* the read-only store runs but isn't
  made additive — run dev-shell tools by name.

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
