// Package nsmount injects bind mounts into a running container by entering its
// mount namespace (see docs/mechanisms.md). It is the consumer half
// of the dynamic design: the createRuntime hook calls BindAll to bind-mount the
// dev-shell closure read-only into the container the runtime just created.
//
// Only the mount namespace (CLONE_NEWNS) is entered. That is permitted from a
// multithreaded Go process and suffices whenever the hook already holds
// CAP_SYS_ADMIN in the user namespace owning that mount ns. On EPERM, BindAll
// returns a descriptive error and the caller (the hook) degrades gracefully.
//
// The namespace switch and bind mounts run in a short-lived CHILD process (the
// hook re-execs itself with ChildSubcommand), not on a goroutine inside the
// hook. setns(CLONE_NEWNS) taints the calling OS thread, and having the Go
// runtime destroy that tainted thread while the hook keeps running is
// kernel/version-fragile — a fault there can kill the whole hook with a signal
// that no recover() catches, which fails the container. Isolating it in a child
// means any such fault dies with the child (best-effort, ignored), while the
// mounts — created in the container's mount namespace — persist after the child
// exits because the container's own processes keep that namespace alive.
package nsmount

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// ChildSubcommand is the hidden argv[1] the hook uses to re-exec itself as the
// mount child. main dispatches it to RunChild.
const ChildSubcommand = "__nsmount"

// BindAll bind-mounts each path in closure read-only onto rootfs+path inside the
// container at pid by re-execing this binary as a short-lived mount child (see
// the package doc for why a child rather than a goroutine). Source paths (host
// /nix/store/...) are reachable from the container mount ns before pivot_root,
// and mounts created under rootfs survive pivot_root, appearing at /path.
func BindAll(pid int, rootfs string, closure []string) error {
	if pid <= 0 {
		return fmt.Errorf("nsmount: invalid pid %d", pid)
	}
	if rootfs == "" {
		return fmt.Errorf("nsmount: empty rootfs")
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("nsmount: locate self: %w", err)
	}
	cmd := exec.Command(self, ChildSubcommand, strconv.Itoa(pid), rootfs)
	// Closure paths go over stdin (newline-separated) to avoid ARG_MAX limits
	// on large closures.
	cmd.Stdin = strings.NewReader(strings.Join(closure, "\n"))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("nsmount child: %w: %s", err, msg)
		}
		return fmt.Errorf("nsmount child: %w", err)
	}
	return nil
}

// RunChild is the entry point for the re-exec'd mount child: it reads pid and
// rootfs from args and the newline-separated closure from stdin, then performs
// the bind mounts on a locked, fs-detached thread. The caller (main) maps a
// non-nil error to a non-zero exit and then exits the process, so the tainted
// thread is reclaimed by ordinary process teardown rather than Go's per-thread
// destruction.
func RunChild(args []string, stdin io.Reader) error {
	if len(args) != 2 {
		return fmt.Errorf("nsmount child: usage: %s <pid> <rootfs>", ChildSubcommand)
	}
	pid, err := strconv.Atoi(args[0])
	if err != nil {
		return fmt.Errorf("nsmount child: bad pid %q: %w", args[0], err)
	}
	rootfs := args[1]
	data, err := io.ReadAll(stdin)
	if err != nil {
		return fmt.Errorf("nsmount child: read closure: %w", err)
	}
	var closure []string
	for _, line := range strings.Split(string(data), "\n") {
		if line != "" {
			closure = append(closure, line)
		}
	}

	errc := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				errc <- fmt.Errorf("nsmount: panic: %v", r)
			}
		}()
		// Locked and never unlocked: setns taints this thread. The child exits
		// right after, so the thread is torn down with the process.
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
		// namespace. The hook treats this as best-effort and continues.
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
