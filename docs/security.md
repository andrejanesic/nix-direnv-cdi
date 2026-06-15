# Security model

## The gate: `DIRENV_DIR` activates the hook

The hook acts only when `DIRENV_DIR` is present in the environment it inherits
from the container runtime — i.e. when you launched the container **from inside
the project's loaded dev-shell**. Run the device from anywhere else (a plain
shell, a daemon, a different project) and the hook **no-ops**: nothing is
mounted, `PATH`/env are untouched, and the container runs exactly as if the
device weren't attached.

Treat `DIRENV_DIR` as an **activation switch, not an authorization boundary**. It
decides *whether* the hook runs and *which* project's `mounts.json` it reads — it
does not, on its own, prove the caller is trusted. For daemon-driven Docker you
may even pass `DIRENV_DIR`/`DIRENV_DIFF` explicitly with `--env` (see below), so
any caller who can set an environment variable on the run can open the gate. What
actually bounds exposure is that the hook only ever acts **as the launching
user, on that user's own files, into that user's own container**, and that it
refuses to bind-mount anything outside `/nix/store`. The trust assumptions are
spelled out in the threat model below.

## Threat model

`nix-direnv-cdi` runs entirely as the user who launches the container, on that
user's own files, into that user's own container. It holds no privilege the
launcher doesn't already have, and it never writes secrets to disk (see below).

**Trusted — you rely on these being honest:**

- **You**, the launching user, and your login environment.
- **The project directory** and its `.direnv/`, in particular
  `.direnv/cdi/mounts.json` (the list of `/nix/store` paths the hook bind-mounts).
- **The `.envrc`** you ran `direnv allow` on — `direnv allow` already runs
  arbitrary code as you, so only allow `.envrc` files you trust.
- **The Nix store** the closure resolves to.

**Not relied upon:**

- **Unrelated containers** — the hook runs only on containers carrying the CDI
  device via `--device`.
- **The container image / entrypoint** — a hostile image cannot make the hook
  exceed the launcher's own privileges.
- **`mounts.json` contents** — the hook refuses any entry that is not a clean,
  absolute path under `/nix/store` (or `$NIX_STORE_DIR` for a relocated store),
  so a corrupted or tampered `mounts.json` cannot redirect the bind-mount at an
  arbitrary host path.

On a **single-user workstation** every trusted item is yours, so exposure is
self-to-self: the hook can only do what you could already do with `-v`/`--env`.

## Shared hosts and multi-user systems

The trust assumptions above are really about *who can write the project directory
and set the launch environment*. On a shared or multi-user host:

- **Do not launch from a group-/world-writable project directory.** If another
  user can write `.direnv/cdi/mounts.json` they choose which `/nix/store` paths
  are mounted into *your* container — and although those paths are validated to
  be store paths, a store path they populated can still carry binaries that
  shadow tools on your container `PATH`. Keep `.direnv` private: `chmod 0700 .direnv`.
- **Treat `DIRENV_DIR`/`DIRENV_DIFF` as inputs, not credentials.** Anyone who can
  set them on a run can open the gate; that is by design for Docker. The
  protections are the `/nix/store` validation and the launcher-only privilege.
- **`mounts.json` is world-readable (`0644`)**, so it discloses your closure
  structure (not secrets) to other local users.
- **Debug logging (`NDC_HOOK_LOG`)** should point to a private path; it is
  created `0600` and does not follow symlinks.

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

Docker users may pass `DIRENV_DIR` and `DIRENV_DIFF` through with `--env` so the
daemon-created OCI process exposes the loaded direnv context to the hook. The
wrapper unsets those bookkeeping variables before it execs the real entrypoint;
they are an input to the hook, not part of the intended dev-shell environment.

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

## Verifying releases

Release binaries are checksummed, cosign-signed (keyless), and carry GitHub
build-provenance attestations. Verify them before installing — see
[release.md → Verify artifacts](release.md#verify-artifacts) for the exact
`sha256sum -c`, `gh attestation verify`, and `cosign verify-blob` commands and
the expected workflow identity / OIDC issuer.

## See also

- [limitations.md](limitations.md) — limitations, runtime support, and non-goals.
- [internals.md](internals.md) — the low-level behaviours behind the above (e.g.
  why the read-only remount is best-effort under rootless).
- [release.md](release.md) — release channels and artifact verification.
