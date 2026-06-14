# nix-direnv-cdi

Make a project's **nix-direnv dev-shell** available inside **any OCI container**
(podman, docker) with a single `--device`. **One** generic CDI device serves
every project; a `createRuntime` hook injects the project's dev-shell — the
read-only `/nix/store` closure, an additive `PATH`, and the dev-shell env —
**dynamically at container-creation time**, reading what it needs from the
loaded direnv environment it inherits.

```sh
# in a project whose .envrc has loaded its dev-shell:
podman run --device "$DIRENV_CDI" busybox hello
# Hello, world!     ← `hello` came from the dev-shell, not the image
```

## How it works

- **`install`** registers one generic device, `nix-direnv.cdi/shell=devshell`. It
  carries no project data — only the hook.
- **`gen`** (run in your project, e.g. from `.envrc`) writes the dev-shell's
  store closure to `.direnv/cdi/mounts.json`.
- At **`podman run --device …`** the hook — launched from your loaded dev-shell,
  so it inherits `DIRENV_DIR`/`DIRENV_DIFF` — gates on being in an approved
  dev-shell, bind-mounts the closure into the container by entering its mount
  namespace, and wraps the entrypoint so `PATH` is additive and the dev-shell
  env is exported. Outside the loaded shell the device is **inert**.

No per-project devices, no fingerprints, nothing baked: the same `$DIRENV_CDI`
works for every project, and the right closure is chosen at run time.

## Install (once per machine)

```sh
nix run github:andrejanesic/nix-direnv-cdi -- install
# or: nix profile install github:andrejanesic/nix-direnv-cdi && nix-direnv-cdi install
```

`install` writes the generic device to `~/.config/cdi` and registers that
directory with podman (a `containers.conf.d` drop-in) and, if present, docker
(`/etc/docker/daemon.json` — needs root + a daemon restart; the exact change is
printed if it can't apply it).

## Per project

In the project's `.envrc`:

```sh
use flake
eval "$(nix-direnv-cdi gen)"   # writes .direnv/cdi/mounts.json and exports $DIRENV_CDI
```

or, with the `use_cdi` helper (copy `contrib/use_cdi.sh` into
`~/.config/direnv/direnvrc`):

```sh
use flake
use cdi
```

`gen` re-runs on every direnv reload, so the closure stays in sync as
dependencies change. `.direnv/` is already gitignored.

## Run

From the project's dev-shell:

```sh
podman run --device "$DIRENV_CDI" <image> <cmd>
```

docker compose (CDI device reservation):

```yaml
services:
  app:
    image: busybox
    deploy:
      resources:
        reservations:
          devices:
            - driver: cdi
              device_ids: ["nix-direnv.cdi/shell=devshell"]
```

## Notes & limitations

- **Authorization model:** being in the loaded dev-shell *is* the gate. Run the
  device from anywhere else and it does nothing — by design, we don't expose a
  dev-shell you haven't entered (and thus approved via `direnv allow`).
- **Runtimes:** verified on rootless **podman** (crun) and **runc**. Docker uses
  runc, so it is expected to work; a real moby end-to-end smoke test is pending.
- **Read-only:** closure mounts are best-effort read-only. Under a rootless user
  namespace the ro-remount is refused, but nix store paths are immutable and
  mode `0555` on the host, so they're effectively read-only regardless.
- **Known limitation (T9):** an absolute entrypoint that is a path *into* the
  read-only store (e.g. `… /nix/store/…/bin/tool`) runs but its `PATH` is not
  made additive. Run dev-shell tools by name.

See [PLAN.md](PLAN.md) for the full design and the findings behind it.
