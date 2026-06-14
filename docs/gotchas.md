# Gotchas & load-bearing hacks

The non-obvious, hard-won details the implementation depends on. Each was a real
trap; several were only caught by running against a live runtime.

## `CLONE_FS` → `setns(CLONE_NEWNS)` EINVAL (the Go trap)

All Go runtime threads share `CLONE_FS` (cwd/root/umask) state, and the kernel
refuses `setns(CLONE_NEWNS)` with **`EINVAL`** while the caller shares `CLONE_FS`
with other threads. `nsmount` therefore calls `unix.Unshare(unix.CLONE_FS)`
**first**, on a `runtime.LockOSThread()`'d goroutine that is then discarded
(returns without unlocking → the tainted thread dies), keeping every other
thread in the host namespace. Without the `unshare`, the mount silently fails.

## No `CLONE_NEWUSER` from Go → bare rootless runc is out of scope

`setns(CLONE_NEWUSER)` requires a **single-threaded** process. The Go runtime is
always multithreaded, so the userns-entry fallback (needed only for *bare
rootless runc with an unprivileged invoker*) cannot be done in pure Go — it would
need an `nsexec`-style C constructor that runs before the Go runtime starts.
That configuration is a [non-goal](caveats.md#non-goals); the hook no-ops there.
Every real podman/docker config needs only mount-ns entry.

## Host-side mounts don't propagate; you must enter the container's mount ns

A bind mount the hook performs in the **host** mount namespace does not appear
inside the container (its mount ns is private/slave). Only file/dir *writes* into
the rootfs propagate. So the closure must be mounted from *inside* the
container's mount ns (via the `pid` from the OCI State). Source paths are
reachable there pre-`pivot_root`, and mounts under `root.path` survive it.

## Some closure paths are files, not directories

`nix-store -qR` can return single files (e.g. a setup-hook `.sh`), not just
directories. The bind **target must match the source type** — `mkdir` for a dir,
touch an empty file for a file — or `mount --bind` fails with "not a directory".

## Read-only remount is best-effort

A bind mount can't be made read-only in one step; it needs a second `MS_REMOUNT |
MS_RDONLY` call. Under a rootless user namespace that remount is refused
(`EPERM`). We treat it as best-effort: the bind still succeeds, and store paths
are immutable `0555` on the host anyway. See [security.md](security.md).

## `DIRENV_DIFF` format

`DIRENV_DIFF` is **padded URL-safe base64 → zlib → JSON `{"p":prev,"n":next}`**.
The decoder strips trailing `=` and uses raw URL decoding, tolerating both padded
and unpadded forms. The additive-`PATH` prefix is the PATH entries in `n` absent
from `p`; the dev-shell env is the keys in `n` minus `PATH` and `DIRENV_*`.

## `DIRENV_DIFF`/`DIRENV_DIR` are unset during `.envrc` evaluation

direnv computes the diff only *after* `.envrc` finishes, so `DIRENV_DIFF` and
`DIRENV_DIR` are **not available while `.envrc` runs**. This is why:

- **`gen`** derives the closure from the **gcroot** (which `use flake`
  materialises during eval), not from `DIRENV_DIFF` — so it can run in `.envrc`.
- the **hook** reads `DIRENV_DIFF` at *run time*, which is exactly when it *is*
  set (the container is launched from the finalized, loaded shell).

## `0755` traversability (rootless)

The CDI spec dir, the hook binary, and the `mounts.json` path chain must all be
traversable (`>=0755`) or rootless podman reports "unresolvable CDI devices" /
the hook (running as a subuid) can't read `mounts.json`. The tests widen
`t.TempDir()` (which is `0700`) accordingly.

## The hook must always exit 0

A non-zero `createRuntime` hook **aborts the container**. The hook is therefore
strictly best-effort: every failure is logged and swallowed, and `cmdHook`
returns exit 0 regardless. A broken dev-shell injection must never break the
user's container.

## The hook's `PATH` is sanitized

crun resets the hook's `PATH` to a default (e.g. `/usr/local/sbin:…:/bin`), so
the hook can't rely on its own `PATH` to find the dev-shell prefix or tools. It
reads the prefix from `DIRENV_DIFF` (which carries it internally) and uses
absolute paths / direct syscalls. Arbitrary inherited vars (like `DIRENV_DIFF`)
*do* pass through intact.

## Hook path resolves to the immutable store binary

`gen`/`install` embed `os.Executable()` as the hook `path`. When installed via
the flake, `/proc/self/exe` resolves through any profile symlink to the
content-addressed `/nix/store/.../bin/nix-direnv-cdi` — immutable and
`0755`-traversable, exactly what the spec needs.

## Debugging a silent hook: `NDC_HOOK_LOG`

A `createRuntime` hook is invisible (its stdout/stderr aren't surfaced unless it
fails). Set `NDC_HOOK_LOG=<file>` in the launching environment and the hook
appends a trace of the gate decision, mounts.json read, mount result, and
DIRENV_DIFF decode. This is how the `CLONE_FS`, file-vs-dir, and ro-remount bugs
were found.
