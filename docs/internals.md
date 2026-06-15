# Internals

**For maintainers — read this before changing the hook or `nsmount`.** The
non-obvious, hard-won kernel/Go details the implementation depends on. Each was
a real trap; several were only caught by running against a live runtime. (For
user-facing limitations and runtime support, see
[limitations.md](limitations.md).)

## `setns(CLONE_NEWNS)` runs in a child process, not the hook

The namespace switch and bind mounts run in a **short-lived child process** that
the hook re-execs (`nix-direnv-cdi __nsmount <pid> <rootfs>`, closure on stdin —
see `nsmount.BindAll`/`RunChild`), not on a goroutine inside the hook itself.
`setns(CLONE_NEWNS)` permanently taints the calling OS thread, and the original
design left that thread to be destroyed by the Go runtime while the hook kept
running. That per-thread teardown after `setns` is kernel/Go-version-fragile: on
some runtimes (observed on a GitHub Actions rootless-podman runner) it kills the
hook with a signal **no `recover()` can catch**, and crun then fails the whole
container (`error executing hook … (exit code: 1)`). Isolating the work in a
child means any such fault dies with the child (best-effort, ignored by the
parent), while the mounts — created in the *container's* mount namespace — persist
after the child exits, because the container's own processes keep that namespace
alive.

## `CLONE_FS` → `setns(CLONE_NEWNS)` EINVAL (the Go trap)

All Go runtime threads share `CLONE_FS` (cwd/root/umask) state, and the kernel
refuses `setns(CLONE_NEWNS)` with **`EINVAL`** while the caller shares `CLONE_FS`
with other threads. The mount child therefore calls `unix.Unshare(unix.CLONE_FS)`
**first**, on a `runtime.LockOSThread()`'d goroutine (the child exits right
after, so the tainted thread is reclaimed by ordinary process teardown). Without
the `unshare`, the mount silently fails.

## Host-side mounts don't propagate; you must enter the container's mount ns

A bind mount the hook performs in the **host** mount namespace does not appear
inside the container (its mount ns is private/slave) — only file/dir *writes*
into the rootfs propagate. So the closure must be bind-mounted from *inside* the
container's mount ns, via the `pid` from the OCI State. (The propagation and
`pivot_root` mechanics are in
[mechanisms.md](mechanisms.md#2-dynamic-mount-injection-the-closure).)

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
