# Limitations, runtime support & troubleshooting

**For users.** Where the tool runs, what it deliberately doesn't do, and how to
diagnose it when nothing happens. (For the kernel/Go internals behind these
edges, see [internals.md](internals.md).)

## Runtime support

| Configuration | Status |
|---|---|
| **podman** | ✅ verified end-to-end |
| **docker** | ✅ verified end-to-end |

The shipped hook does **mount-namespace-only** entry, which is sufficient
wherever the hook holds `CAP_SYS_ADMIN` in the userns owning the container's
mount ns — i.e. podman and docker configurations.

> **Docker** must have CDI enabled — on by default since Docker 28.3 (opt-in via
> the `cdi` feature in 25.0–28.1). Podman supports CDI out of the box.
> Docker users must pass the direnv bookkeeping variables through to the OCI
> process env, for example `--env DIRENV_DIR --env DIRENV_DIFF`, because the
> daemon may not inherit the client shell's loaded direnv environment.

## Troubleshooting

**The device is attached but nothing happens** (the container runs, but the
dev-shell isn't there):

- **Are you in the loaded dev-shell?** `echo $DIRENV_DIR` must be non-empty — if
  it's empty the gate is closed *by design* (see [security.md](security.md)).
- **Using `sudo`?** Add `-E` (`sudo` strips `DIRENV_DIR`/`DIRENV_DIFF`).
- **Did `gen` run?** `.direnv/cdi/mounts.json` must exist and be current — re-run
  `nix-direnv-cdi gen`, or reload direnv.
- **Is the device found?** Run `nix-direnv-cdi install` once, or pass
  `--cdi-spec-dir ~/.config/cdi` explicitly.
- **Still stuck?** Set `NDC_HOOK_LOG=/tmp/ndc-hook.log` in the launching
  environment and read the hook's trace (gate decision, mounts, `DIRENV_DIFF`).

## Limitations

- **T9 — absolute path into the read-only store is not made additive.** If the
  container's entrypoint is an absolute path *into* a mounted store path
  (e.g. `… /nix/store/…/bin/tool`), it runs but its `PATH` is not made additive
  (crun execs it directly; it can't be wrapped in place on a read-only mount).
  **Mitigation:** run dev-shell tools by name, not by absolute store path.
- **Read-only is best-effort under rootless.** The ro-remount is refused in a
  rootless user namespace; the bind is read-write, but store paths are immutable
  `0555` so they're effectively read-only. Rootful gets true read-only. (Why:
  [internals.md](internals.md).)
- **Freshness.** The closure is captured at `gen` time. If you change
  dependencies, re-run `gen` (a direnv reload does this automatically) so
  `mounts.json` matches the dev-shell.
- **Prefix entries outside `/nix/store`.** A `DEVSHELL`-style prefix entry that
  isn't a store path (e.g. nix-direnv's `.direnv/bin`) is on the additive `PATH`
  but isn't part of the mounted closure, so tools there won't resolve in the
  container. The common case (store `bin` dirs) is covered.
- **No workdir mount.** Project sources aren't mounted; add `-v $PWD:$PWD`.
- **`sudo` strips the gate env** → device inert; use `sudo -E`. See the
  Troubleshooting note above and [security.md](security.md) for why.

## Non-goals

- **Mounting the entire `/nix/store`.** Rejected: too broad an exposure. We mount
  only the project's surgical closure.
- **podman `precreate` / custom runtime shims.** podman-only or heavier; we stay
  on standard, cross-runtime CDI hooks.
- **Non-host-accessible prefixes.** The mechanism assumes the dev-shell prefix is
  host-accessible bind-mountable paths (true for nix store closures).
