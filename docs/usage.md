# Usage — install, set up, run, remove & troubleshoot

**For users.** Everything you need to install `nix-direnv-cdi`, wire it into a
project, run containers against your dev-shell, and remove it again — plus a
troubleshooting section for when nothing seems to happen.

For a hands-on, copy-pasteable project (running a coding agent in a container),
see [`../example/`](../example/readme.md). For the *why* behind the design, see
[architecture.md](architecture.md) and [decisions.md](decisions.md).

## What you get

One generic CDI device, `nix-direnv-cdi.org/env=current`, that makes the
**current project's nix-direnv dev-shell** available inside any OCI container.
The device holds no project data — at `run` time its `createRuntime` hook reads
the direnv environment you're already in, bind-mounts that dev-shell's
`/nix/store` closure into the container (read-only, surgical — never the whole
store), and prepends the dev-shell's `bin` dirs to `PATH`. The same device
serves every project; the launching shell decides which dev-shell.

## Prerequisites

- **Nix**, **direnv**, and **nix-direnv**
- **flakes enabled** if your project uses `use flake`
- **podman**, or **Docker Engine** with CDI support

> Docker must have CDI enabled. Docker's docs say CDI is Linux-only and on by
> default since Docker Engine 28.3.0; older versions may need the `cdi` feature
> enabled explicitly. Podman supports CDI out of the box.

## Install

The device is just one generic CDI spec plus the registration that lets the
runtime find it. **On NixOS and home-manager, declare it** — that's the
preferred path (next two sections). On other distros, use the imperative
`install` command.

> **Why declarative on Nix?** The imperative `nix-direnv-cdi install` writes the
> spec **once**, recording the hook binary's store path at that moment. On Nix
> that path moves on every upgrade, so the once-written spec goes stale and you'd
> have to re-run `install`. Declaring the spec instead ties the hook path to the
> flake package (`${ndc}/bin/nix-direnv-cdi`): every `nixos-rebuild` /
> `home-manager switch` **regenerates** the file for the installed version, so
> it's always correct and never dangles — install once, never re-run.

### NixOS (recommended)

Add the flake as an input, then in your system configuration:

```nix
# flake.nix
inputs.nix-direnv-cdi.url = "github:andrejanesic/nix-direnv-cdi";
```

Both modules below take `inputs` as an argument, so make `inputs` available to
your modules (skip if you already thread it through):

- **NixOS** — pass `specialArgs = { inherit inputs; };` to
  `nixpkgs.lib.nixosSystem { ... }`.
- **home-manager** — pass `extraSpecialArgs = { inherit inputs; };` to
  `home-manager.lib.homeManagerConfiguration { ... }` (or to the
  `home-manager.extraSpecialArgs` option when used as a NixOS module).

(Alternatively, drop the `inputs` arg and reference the package through an
overlay or a `let` binding in scope.)

```nix
{ pkgs, inputs, ... }:
let
  ndc = inputs.nix-direnv-cdi.packages.${pkgs.system}.default;

  # The single generic device. The hook path is the package's own store path, so
  # this spec is a precise dependency: each rebuild regenerates it for the
  # installed version, and that path can't be GC'd while this generation exists.
  # (Same JSON `nix-direnv-cdi install` would write, minus the staleness.)
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

  # /etc/cdi is a default CDI scan dir for BOTH podman and docker, so one file
  # serves both — no podman containers.conf.d drop-in needed.
  environment.etc."cdi/nix-direnv.json".source = ndcSpec;

  # docker only: CDI is opt-in before Docker 28.3 (and for rootless docker).
  virtualisation.docker.daemon.settings.features.cdi = true;
  # rootless docker instead:
  # virtualisation.docker.rootless.daemon.settings.features.cdi = true;
}
```

`nixos-rebuild switch` installs the binary and the device. Nothing else to run —
skip straight to [Set up a project](#set-up-a-project).

### home-manager (recommended)

home-manager manages your home, not `/etc` or the docker daemon, so this covers
**rootless podman**. (For docker, configure it at the system level as above.)

```nix
{ config, pkgs, inputs, ... }:
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
  home.packages = [ ndc ]; # puts the `gen` CLI on $PATH

  xdg.configFile."cdi/nix-direnv.json".source = ndcSpec;

  # ~/.config/cdi is NOT a default scan dir, so register it for rootless podman.
  xdg.configFile."containers/containers.conf.d/nix-direnv-cdi.conf".text = ''
    [engine]
    cdi_spec_dirs = ["/etc/cdi", "/var/run/cdi", "${config.xdg.configHome}/cdi"]
  '';
}
```

> The hardcoded `cdiVersion = "0.3.0"` matches the generic device's shape (one
> `createRuntime` hook, no env/mounts). It is stable by design. If you ever want
> to confirm the current format, diff against the output of
> `nix-direnv-cdi install` (it writes the same JSON to `~/.config/cdi/`).

### Imperative install (other Linux distros)

On non-NixOS systems, install the binary, then run `install` once to write and
register the device on this machine.

**Nix profile (pinned):**

```sh
nix profile install github:andrejanesic/nix-direnv-cdi/v0.1.0
nix-direnv-cdi install
```

**One-shot, without adding to a profile:**

```sh
nix run github:andrejanesic/nix-direnv-cdi/v0.1.0 -- install
```

**Standalone binary:**

```sh
install -m 0755 nix-direnv-cdi_v0.1.0_linux_amd64 ~/.local/bin/nix-direnv-cdi
nix-direnv-cdi install
```

Re-run `nix-direnv-cdi install` after every install or upgrade. The CDI spec
embeds the installed binary path as the hook path, so the registration must be
refreshed whenever the package path changes. (This is the staleness the NixOS
and home-manager paths above avoid.)

`install` writes (and registers) the device:

- **podman** — the spec at `~/.config/cdi/nix-direnv.json` plus a podman drop-in
  at `~/.config/containers/containers.conf.d/nix-direnv-cdi.conf`.
- **docker** — the daemon-scanned system spec at `/etc/cdi/nix-direnv.json`. If
  that write needs privileges, the command prints the exact manual fallback
  (install the generated `~/.config/cdi/nix-direnv.json` with
  `sudo install -D -m 0644`).

For artifact verification (checksums, cosign, provenance, SBOM), upgrade, and
package rollback, see [release.md](release.md).

## Set up a project

In the project's `.envrc`:

```sh
use flake
nix-direnv-cdi gen          # writes .direnv/cdi/mounts.json
```

`gen` records the dev-shell's `/nix/store` closure to `.direnv/cdi/mounts.json`.
A direnv reload re-runs it, so the closure stays in sync when dependencies
change.

Prefer the helper? Copy [`../contrib/use_cdi.sh`](../contrib/use_cdi.sh) into
`~/.config/direnv/direnvrc` and write:

```sh
use flake
use cdi
```

Then `direnv allow`.

**Smoke test** from inside the loaded dev-shell:

```sh
podman run --rm --device nix-direnv-cdi.org/env=current busybox true
```

Exit status 0 with no output proves the device resolves and the inert hook
doesn't break a plain container. Swap `true` for a command that exists in your
dev-shell but not the image to confirm propagation.

## Run containers

The device reference is always the constant `nix-direnv-cdi.org/env=current`.

A dev-shell tool running inside a stock image that doesn't ship it:

```console
$ podman run --rm --device nix-direnv-cdi.org/env=current alpine go version
go version go1.22.0 linux/amd64    # ← go came from your dev-shell, not alpine
```

Run your real toolchain against your sources — mount them yourself with
`-v "$PWD:$PWD"` (sources are never mounted for you):

```sh
podman run --device nix-direnv-cdi.org/env=current -v "$PWD:$PWD" -w "$PWD" alpine \
  go test ./...
```

**Docker** runs containers through a daemon that may not inherit your shell's
loaded direnv environment, so pass the bookkeeping variables through explicitly:

```sh
docker run --env DIRENV_DIR --env DIRENV_DIFF \
  --device nix-direnv-cdi.org/env=current -v "$PWD:$PWD" -w "$PWD" <image> <cmd>
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

### Common use-cases

- **Coding agents in containers.** Run an agent in a disposable container with
  your real, pinned toolchain — no per-repo agent image, and the agent's
  environment is provably identical to yours. Mount sources writable with
  `-v "$PWD:$PWD"`, and scrub secrets you don't want it to see from the
  dev-shell env. Worked example: [`../example/`](../example/readme.md).
- **Debug a distroless / scratch image.** Attach the device and your dev-shell's
  debug toolkit (`curl`, `gdb`, `strace`, …) appears read-only — inspect the
  *real* image, no debug variant to build.
- **Reproducible CI / "works on my machine".** CI and your laptop run the exact
  same nix-pinned tools under one generic runner image.
- **Cross-distro / cross-libc testing.** Hold test tooling fixed and sweep the
  base image (glibc vs musl, old vs new distro) under it.

## Remove

Unregister the machine-level device this tool owns:

```sh
nix-direnv-cdi uninstall
```

This removes only the owned artifacts — the generic CDI spec file, the podman
drop-in, and the Docker system CDI spec. It leaves the shared CDI directory in
place and creates no backups. Per-project state is just `.direnv/cdi/`; delete
it with the rest of `.direnv` when you tear down a project.

Removing the **package** is separate from unregistering the device:

```sh
nix profile remove nix-direnv-cdi          # Nix profile install
# or remove ~/.local/bin/nix-direnv-cdi    # standalone binary
```

For package rollback (profile generations, reinstalling a pinned release), see
[release.md](release.md#rollback).

## Troubleshooting

**The device is attached but nothing happens** — the container runs, but the
dev-shell isn't there:

- **Are you in the loaded dev-shell?** `echo $DIRENV_DIR` must be non-empty. If
  it's empty the gate is closed *by design* — see [security.md](security.md).
- **Using `sudo`?** Add `-E` (or `--preserve-env=DIRENV_DIR,DIRENV_DIFF`).
  Plain `sudo` strips `DIRENV_DIR`/`DIRENV_DIFF` and the device goes inert.
- **Did `gen` run?** `.direnv/cdi/mounts.json` must exist and be current —
  re-run `nix-direnv-cdi gen`, or reload direnv.
- **Using Docker?** Pass the direnv variables explicitly:
  `docker run --env DIRENV_DIR --env DIRENV_DIFF --device nix-direnv-cdi.org/env=current …`.
- **Is the device found?** Run `nix-direnv-cdi install` once. Podman reads the
  user shared CDI dir registered by the drop-in; Docker reads
  `/etc/cdi/nix-direnv.json`.
- **Relocated Nix store?** The hook only bind-mounts closure paths under
  `/nix/store`. If your store lives elsewhere, set `NIX_STORE_DIR` in the
  launching environment (and pass it through with `--env NIX_STORE_DIR` on
  Docker).
- **Still stuck?** Set `NDC_HOOK_LOG` (e.g. `$XDG_RUNTIME_DIR/ndc-hook.log`) and
  read the hook's trace:
  - `gate closed` — `DIRENV_DIR` was not visible to the hook.
  - a `mounts.json` read error — `gen` hasn't run, the file is stale, or its
    path isn't traversable by the hook.
  - `mount FAILED` — closure injection failed (the hook still exits 0).
  - a `DIRENV_DIFF` decode error — the dev-shell env couldn't be decoded for
    additive `PATH`/env injection.

### Known limitations

- **Absolute path into the store isn't made additive (T9).** If the entrypoint
  is an absolute path *into* a mounted store path, it runs but its `PATH` isn't
  made additive (it can't be wrapped in place on a read-only mount). Run
  dev-shell tools **by name**, not by absolute store path.
- **Read-only is best-effort under rootless.** The ro-remount is refused in a
  rootless userns; the bind is read-write, but store paths are immutable `0555`,
  so effectively read-only. Rootful gets true read-only.
- **Sources aren't mounted.** Add `-v "$PWD:$PWD"` yourself.
- **Prefix entries outside `/nix/store`** (e.g. nix-direnv's `.direnv/bin`) land
  on the additive `PATH` but aren't in the mounted closure, so tools there won't
  resolve in the container. Store `bin` dirs (the common case) are covered.

Full limitations, the runtime support matrix, and non-goals:
[limitations.md](limitations.md). Kernel/Go internals behind these edges:
[internals.md](internals.md).
