# Security model

## The gate *is* the authorization

The hook acts only when `DIRENV_DIR` is present in the environment it inherits
from the container runtime — i.e. when you launched the container **from inside
the project's loaded dev-shell**. Being in that shell means you ran
`direnv allow` and entered it; that approval *is* the authorization to expose the
dev-shell.

Run the device from anywhere else (a plain shell, a daemon, a different project)
and the hook **no-ops**: nothing is mounted, `PATH`/env are untouched, and the
container runs exactly as if the device weren't attached. We deliberately do not
make a dev-shell available to a context that hasn't entered (and thus approved)
it.

## Opt-in, no blast radius

The mechanism is a **CDI device you explicitly attach** with `--device`. The
hook runs only on containers carrying that device — never on unrelated
containers. (This is a deliberate advantage over a podman `precreate` /
`when: always` hook, which would sit in the path of *every* container.)

## What is exposed, and how

- **Surgical closure only.** The container gets this project's dev-shell store
  closure — the exact `/nix/store` paths from `nix-store -qR`, nothing more. We
  do **not** mount the whole `/nix/store`.
- **Read-only (best-effort).** Closure mounts are bind-mounted read-only where
  the kernel allows it. Under a rootless user namespace the read-only *remount*
  is refused, so the bind is read-write — but nix store paths are immutable and
  mode `0555` on the host, so the container's uid cannot write them regardless.
  Under rootful the remount succeeds and the mounts are truly read-only.
- **No workdir mount.** Your project sources are not mounted; add `-v $PWD:$PWD`
  yourself if you want them.

## Secrets stay off disk

The dev-shell's environment (which can include secrets) is **never written to
the CDI spec or any file**. It is decoded from the live `DIRENV_DIFF` at
container-creation time and injected only into the wrapped entrypoint's
environment. The on-disk artifacts are just the generic device (a hook path) and
`mounts.json` (a list of store paths) — no secrets.

## Privilege

The hook runs with the privilege of whoever launched the container:

- **rootless** (podman/RootlessKit-docker): as your mapped subuid, inside the
  container's user namespace — enough to enter the mount ns and bind host-readable
  store paths. It cannot do anything you couldn't already do.
- **rootful**: as root. Reads/binds the same host store paths; the read-only
  remount succeeds.

The hook is **best-effort and always exits 0** — a failure can never break or
escalate; the worst case is the dev-shell simply isn't injected.

## `sudo` caveat (rootful)

`sudo` strips the environment by default, so `sudo podman run …` loses
`DIRENV_DIR`/`DIRENV_DIFF` → the gate is closed → the device is inert. Use
`sudo -E` (or `--preserve-env=DIRENV_DIR,DIRENV_DIFF`), or a root shell that has
the dev-shell loaded. This is a usage note, but also a property: without the
approved environment, nothing is exposed.

## See also

- [limitations.md](limitations.md) — limitations, runtime support, and non-goals.
- [internals.md](internals.md) — the low-level behaviours behind the above (e.g.
  why the read-only remount is best-effort under rootless).
