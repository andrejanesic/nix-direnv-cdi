// Package nsmount injects bind mounts into a running container by entering its
// mount namespace (see docs/mechanisms.md). It is the consumer half
// of the dynamic design: the createRuntime hook calls BindAll to bind-mount the
// dev-shell closure read-only into the container the runtime just created.
//
// Only the mount namespace (CLONE_NEWNS) is entered. That is permitted from a
// multithreaded Go process and suffices whenever the hook already holds
// CAP_SYS_ADMIN in the user namespace owning that mount ns — the case for
// rootless podman/crun (verified), rootful docker, and rootless docker
// (RootlessKit). The CLONE_NEWUSER fallback needed for an unprivileged invoker
// under bare rootless runc cannot be done in pure Go (setns(CLONE_NEWUSER)
// requires a single-threaded process, which the Go runtime never is); that is
// deferred. On EPERM, BindAll returns a descriptive error and the caller (the
// hook) degrades gracefully.
package nsmount

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"golang.org/x/sys/unix"
)

// BindAll enters pid's mount namespace and bind-mounts each path in closure
// read-only onto rootfs+path inside the container. Source paths (host
// /nix/store/...) are reachable from the container mount ns before pivot_root,
// and mounts created under rootfs survive pivot_root, appearing at /path.
//
// The namespace switch runs on a dedicated locked OS thread that is destroyed
// afterward (the goroutine returns without unlocking), so the caller's threads
// stay in the host namespace.
func BindAll(pid int, rootfs string, closure []string) error {
	if pid <= 0 {
		return fmt.Errorf("nsmount: invalid pid %d", pid)
	}
	if rootfs == "" {
		return fmt.Errorf("nsmount: empty rootfs")
	}
	errc := make(chan error, 1)
	go func() {
		// Intentionally NOT unlocked: setns taints this thread, so we let the
		// goroutine return without unlocking and the Go runtime destroys the
		// thread — keeping every other thread in the host namespace.
		runtime.LockOSThread()
		errc <- bindAllOnThread(pid, rootfs, closure)
	}()
	return <-errc
}

func bindAllOnThread(pid int, rootfs string, closure []string) error {
	// All Go runtime threads share CLONE_FS state (cwd/root/umask), and the
	// kernel refuses setns(CLONE_NEWNS) with EINVAL while the caller shares
	// CLONE_FS with other threads. Detach this thread's fs state first; the
	// thread is locked and discarded after, so the detachment is harmless.
	if err := unix.Unshare(unix.CLONE_FS); err != nil {
		return fmt.Errorf("unshare CLONE_FS: %w", err)
	}
	mntPath := fmt.Sprintf("/proc/%d/ns/mnt", pid)
	fd, err := unix.Open(mntPath, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", mntPath, err)
	}
	defer unix.Close(fd)
	if err := unix.Setns(fd, unix.CLONE_NEWNS); err != nil {
		// EPERM here means the hook lacks CAP_SYS_ADMIN in the container's user
		// namespace (e.g. bare rootless runc with an unprivileged invoker); the
		// CLONE_NEWUSER fallback is not available in pure Go.
		return fmt.Errorf("setns mnt (pid %d): %w", pid, err)
	}
	var firstErr error
	for _, src := range closure {
		if err := bindRO(src, filepath.Join(rootfs, src)); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// bindRO bind-mounts src onto dst, then best-effort remounts it read-only.
// The bind is required; the ro-remount is not: a bind mount can't be made ro in
// one step, and the second-step remount is refused (EPERM) under a rootless
// user namespace. Since nix store paths are immutable and mode 0555 on the host
// (the container's uid can't write them regardless), a rw bind is effectively
// read-only, so a refused ro-remount must not fail the mount.
func bindRO(src, dst string) error {
	// The bind target must match the source type: a closure path is usually a
	// directory but can be a single file (e.g. a setup-hook .sh).
	fi, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat %s: %w", src, err)
	}
	if fi.IsDir() {
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dst, err)
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(dst), err)
		}
		f, err := os.OpenFile(dst, os.O_CREATE, 0o644)
		if err != nil {
			return fmt.Errorf("touch %s: %w", dst, err)
		}
		f.Close()
	}
	if err := unix.Mount(src, dst, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("bind %s -> %s: %w", src, dst, err)
	}
	// Best-effort read-only (ignored on EPERM under rootless userns).
	_ = unix.Mount("", dst, "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY|unix.MS_REC, "")
	return nil
}
