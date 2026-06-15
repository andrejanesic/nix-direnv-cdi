# nix-direnv-cdi

[![ci](https://github.com/andrejanesic/nix-direnv-cdi/actions/workflows/ci.yaml/badge.svg)](https://github.com/andrejanesic/nix-direnv-cdi/actions/workflows/ci.yaml) [![codeql](https://github.com/andrejanesic/nix-direnv-cdi/actions/workflows/codeql.yaml/badge.svg)](https://github.com/andrejanesic/nix-direnv-cdi/actions/workflows/codeql.yaml) [![release](https://img.shields.io/github/v/release/andrejanesic/nix-direnv-cdi?sort=semver)](https://github.com/andrejanesic/nix-direnv-cdi/releases) [![license](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

**Sandbox a coding agent in a throwaway container with your real, pinned project
toolchain — no per-repo agent image to build or maintain.**

Simple, fast and secure.

```sh
# docker
docker run \
  --env DIRENV_DIR \
  --env DIRENV_DIFF \
  --device nix-direnv-cdi.org/env=current -v "$PWD:$PWD" -w "$PWD" \
  ubuntu claude  # Claude now has access to your shell.nix tools

# podman
podman run \
  --device nix-direnv-cdi.org/env=current -v "$PWD:$PWD" \
  -w "$PWD" \
  ubuntu codex  # Codex now has access to your shell.nix tools
```

That's it — no custom Docker images or in-container direnv required.
nix-direnv-cdi automatically mounts the required Nix store paths from your
host machine and updates the entrypoint PATH, so your agent sees the exact
same tools you use in your own shell. Safe (read-only mounts), automatic
and fast.

## Use it

A tool that exists only in your dev-shell, running inside a stock image:

```console
$ podman run --rm --device nix-direnv-cdi.org/env=current busybox hello
Hello, world!          # ← `hello` came from your dev-shell, not from busybox
```

Run your real toolchain against your sources in a minimal image — `go` here comes
from the dev-shell; `alpine` doesn't ship it:

```sh
podman run --device nix-direnv-cdi.org/env=current -v "$PWD:$PWD" -w "$PWD" alpine \
  go test ./...
```

**Docker** runs containers through a daemon, so pass direnv's bookkeeping
variables through to the OCI process:

```sh
docker run --env DIRENV_DIR --env DIRENV_DIFF \
  --device nix-direnv-cdi.org/env=current <image> <cmd>
```

Or with Compose:

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

The device reference is always the constant `nix-direnv-cdi.org/env=current`.

## Install

Prerequisites:

- Nix, direnv, and nix-direnv
- flakes enabled if your project uses `use flake`
- podman, or Docker Engine with CDI support

**1. Install once per machine** (writes + registers the one generic device).

**On NixOS / home-manager, declare it** — this is the preferred path: the hook
path is tied to the flake package, so every rebuild regenerates the spec for the
installed version (the imperative installer writes it once and goes stale on
upgrade). Add the flake input
`inputs.nix-direnv-cdi.url = "github:andrejanesic/nix-direnv-cdi";`, then:

```nix
# NixOS — serves podman AND docker (/etc/cdi is a default scan dir for both)
{ pkgs, inputs, ... }:
let
  ndc = inputs.nix-direnv-cdi.packages.${pkgs.system}.default;
  ndcSpec = pkgs.writeText "nix-direnv-cdi.json" (builtins.toJSON {
    cdiVersion = "0.3.0";
    kind = "nix-direnv-cdi.org/env";
    devices = [{
      name = "current";
      containerEdits.hooks = [{
        hookName = "createRuntime";
        path = "${ndc}/bin/nix-direnv-cdi";
        args = [ "nix-direnv-cdi" "hook" ];
      }];
    }];
    containerEdits = { };
  });
in {
  environment.systemPackages = [ ndc ]; # puts the `gen` CLI on $PATH
  environment.etc."cdi/nix-direnv.json".source = ndcSpec;
  virtualisation.docker.daemon.settings.features.cdi = true; # docker only
}
```

For home-manager (rootless podman) the spec goes to `~/.config/cdi` and needs a
`containers.conf.d` drop-in to register it — see
[docs/usage.md](docs/usage.md#home-manager-recommended).

**Other Linux distros** — install the binary and run the imperative installer:

```sh
nix run github:andrejanesic/nix-direnv-cdi -- install
# or pin a release:
nix profile install github:andrejanesic/nix-direnv-cdi/v0.1.0
nix-direnv-cdi install
```

For Docker, `install` writes the daemon-scanned system CDI spec at
`/etc/cdi/nix-direnv.json`. If that write needs privileges, the command prints
the exact manual fallback (install the generated spec from
`~/.config/cdi/nix-direnv.json` with `sudo install -D -m 0644`). Re-run
`install` after every upgrade (the NixOS/home-manager paths above avoid this).

**2. In your project's `.envrc`:**

```sh
use flake
nix-direnv-cdi gen               # writes .direnv/cdi/mounts.json
```

(or copy [`contrib/use_cdi.sh`](contrib/use_cdi.sh) into
`~/.config/direnv/direnvrc` and just write `use flake` then `use cdi`.)

**3. Smoke test** from inside a loaded dev-shell:

```sh
podman run --rm --device nix-direnv-cdi.org/env=current busybox true
```

Exit status 0 with no output proves the device resolves and the inert hook does
not break a plain container. Then swap `true` for a command that exists in your
dev-shell but not the image (the integration fixture ships GNU `hello`).

## More use-cases

- **Debug a distroless / scratch image.** Your prod container ships no shell,
  `curl`, `gdb`, or `strace`. Attach the device and your dev-shell's debug
  toolkit appears read-only — inspect the *real* image, no debug variant to build.
- **Reproducible CI / "works on my machine".** CI and your laptop run the exact
  same nix-pinned tools — one generic runner image, per-project toolchains, no
  custom CI image to maintain.
- **Coding agents in containers.** Run an agent in a disposable container that has
  your real, pinned project toolchain — no per-repo agent image, and the agent's
  environment is provably identical to yours. (Mount sources writable with
  `-v "$PWD:$PWD"`, and scrub secrets from the dev-shell env you don't want it to
  see.)
- **Cross-distro / cross-libc testing.** Hold your test tooling fixed and sweep
  the base image (glibc vs musl, old vs new distro) under it.

## How it works

`install` registers **one** generic CDI device whose only content is a
`createRuntime` hook. `gen` records your dev-shell's `/nix/store` **closure** to
`.direnv/cdi/mounts.json`. When you `podman run --device …` *from the loaded
dev-shell*, the hook — which inherits your direnv environment — **bind-mounts
that closure into the container** (by entering its mount namespace) and **wraps
the entrypoint** so the dev-shell's `bin` dirs are prepended to `PATH` and its
env vars are set. Nothing per-project is baked into the device; the launching
shell decides which dev-shell, at run time.

- **Being in the dev-shell is the authorization.** The device acts only when
  launched from a shell with the project's dev-shell loaded (you ran `direnv
  allow`). Anywhere else it is **inert** — it mounts nothing. See
  [docs/security.md](docs/security.md).
- **Surgical, not the whole store.** Only this project's closure is mounted,
  read-only (best-effort under rootless; nix store paths are immutable anyway).
  Dev-shell env (incl. secrets) is read live and **never written to disk**.
- **Sources aren't mounted.** Add `-v "$PWD:$PWD"` yourself when you need them.
- **Docker passthrough.** Pass `DIRENV_DIR` and `DIRENV_DIFF` through (`--env`,
  or the Compose `environment` keys); the wrapper unsets them before execing the
  real entrypoint.
- **`sudo` strips the gate.** Use `sudo -E` or
  `--preserve-env=DIRENV_DIR,DIRENV_DIFF`, or the device goes inert.
- **Runtimes.** Verified end-to-end on **podman** and **docker**.
- **Limitation (T9).** An absolute path *into* the read-only store runs but isn't
  made additive — run dev-shell tools by name.

If a container runs but the dev-shell is missing, check `echo "$DIRENV_DIR"` is
non-empty, re-run `gen`, and confirm the `--device` flag. Full troubleshooting
and design: **[docs/](docs/readme.md)**.

## Uninstall

```sh
nix-direnv-cdi uninstall
```

Removes only the machine-level registration this tool owns (the generic CDI spec
file, the podman drop-in, and the Docker system CDI spec); it leaves the shared
CDI directory in place and creates no backups. Package rollback is separate —
see [docs/release.md](docs/release.md).

## Documentation

- **[docs/usage.md](docs/usage.md)** — the user guide: install, project setup,
  run, remove, and troubleshooting in one place.
- **[example/](example/readme.md)** — a copy-pasteable project running a coding
  agent in a container via the device.
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
