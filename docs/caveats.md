# Caveats, limitations & runtime support

## Runtime support

| Configuration | Status |
|---|---|
| **rootless podman** (crun) | ✅ verified end-to-end |
| **rootful podman / root** | ✅ supported — *easier* (proper read-only mounts). Use `sudo -E` so the gate env survives |
| **rootless docker** (RootlessKit) | ✅ supported — structurally identical to rootless podman |
| **rootful docker** | ✅ supported (uses runc); real-moby end-to-end smoke test pending |
| **bare rootless `runc`, unprivileged invoker** | ⛔ out of scope (non-goal) — hook no-ops gracefully |

The shipped hook does **mount-namespace-only** entry, which is sufficient
wherever the hook holds `CAP_SYS_ADMIN` in the userns owning the container's
mount ns — i.e. every real podman/docker configuration. See
[gotchas.md](gotchas.md) for why bare rootless runc is the lone exception.

## Limitations

- **T9 — absolute path into the read-only store is not made additive.** If the
  container's entrypoint is an absolute path *into* a mounted store path
  (e.g. `… /nix/store/…/bin/tool`), it runs but its `PATH` is not made additive
  (crun execs it directly; it can't be wrapped in place on a read-only mount).
  **Mitigation:** run dev-shell tools by name, not by absolute store path.
- **Read-only is best-effort under rootless.** The ro-remount is refused in a
  rootless user namespace; the bind is read-write, but store paths are immutable
  `0555` so they're effectively read-only. Rootful gets true read-only.
- **Freshness.** The closure is captured at `gen` time. If you change
  dependencies, re-run `gen` (a direnv reload does this automatically) so
  `mounts.json` matches the dev-shell.
- **Prefix entries outside `/nix/store`.** A `DEVSHELL`-style prefix entry that
  isn't a store path (e.g. nix-direnv's `.direnv/bin`) is on the additive `PATH`
  but isn't part of the mounted closure, so tools there won't resolve in the
  container. The common case (store `bin` dirs) is covered.
- **No workdir mount.** Project sources aren't mounted; add `-v $PWD:$PWD`.
- **`sudo` strips the gate env.** `sudo podman run …` loses
  `DIRENV_DIR`/`DIRENV_DIFF` → device inert. Use `sudo -E`. See
  [security.md](security.md).

## Non-goals

- **Mounting the entire `/nix/store`.** Rejected: too broad an exposure. We mount
  only the project's surgical closure.
- **podman `precreate` / custom runtime shims.** podman-only or heavier; we stay
  on standard, cross-runtime CDI hooks.
- **Bare rootless `runc` with an unprivileged invoker.** Would require a C
  `nsexec` constructor for the user-namespace entry that pure Go can't perform
  (see [gotchas.md](gotchas.md)). The hook degrades gracefully there. Not a
  target, since it isn't how podman or docker run.
- **Non-host-accessible prefixes.** The mechanism assumes the dev-shell prefix is
  host-accessible bind-mountable paths (true for nix store closures).

## Deferred verification

- **Real Docker/moby end-to-end.** Docker's runtime is runc, which is verified
  directly; a one-off `docker run --device "$DIRENV_CDI" …` on a host with
  genuine moby + CDI enabled would close the loop. (The development environment's
  `docker` is a podman shim, so this can't be run there.)
