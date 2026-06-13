// Package hook implements the createRuntime OCI hook that wraps the container
// entrypoint so the dev-shell prefix is prepended to PATH additively. The hook
// runs in the host namespace after the mount namespace and mounts exist, the
// one stage that can both read config.json and write the final rootfs. See
// PLAN.md §1 for the entrypoint-resolution algorithm and its limitations.
//
// This is a faithful port of the proven bash MVP (../cdi-additive-test.sh,
// lines 24-82, 13/13). The algorithm and the documented T9 limitation are
// preserved verbatim; only the accidental bash/jq complexity is eliminated.
package hook

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	oci "github.com/opencontainers/runtime-spec/specs-go"

	"github.com/andrejanesic/nix-direnv-cdi/internal/ociconfig"
)

// Run executes the createRuntime hook:
//
//  1. read the OCI container State from stdin (yields the bundle path),
//  2. load <bundle>/config.json,
//  3. resolve the rootfs (root.path; relative -> joined onto the bundle),
//  4. wrap the entrypoint (wrapEntrypoint) so the dev-shell prefix is prepended
//     to PATH before exec'ing the real entrypoint.
//
// It is best-effort: the caller ignores the returned error and exits 0 so the
// container is never broken. Every "nothing to wrap" condition returns nil; an
// error is reserved for a genuine I/O failure mid-wrap (which the caller logs).
func Run(in io.Reader) error {
	state, err := ociconfig.ReadState(in)
	if err != nil {
		return err
	}
	spec, err := ociconfig.Load(state.Bundle)
	if err != nil {
		return err
	}

	// Resolve the rootfs: root.path may be absolute or relative to the bundle.
	if spec.Root == nil {
		return nil
	}
	rootfs := spec.Root.Path
	if rootfs == "" {
		return nil
	}
	if !filepath.IsAbs(rootfs) {
		rootfs = filepath.Join(state.Bundle, rootfs)
	}

	return wrapEntrypoint(spec, rootfs)
}

// wrapEntrypoint is the testable core of the hook (no container, no runtime).
// It mirrors the MVP hook's case-split:
//
//   - absolute entry -> crun execs it directly, so wrap in place inside the
//     writable rootfs (move the real aside, write a wrapper). An absolute path
//     that is NOT materialized in the rootfs overlay (e.g. into a RO-mounted
//     prefix) is left untouched -> the documented T9 limitation.
//   - relative entry -> resolve command-v-style across prefix:imagePATH, then
//     shadow it with a shim in the first image-PATH dir so crun finds it first.
//
// Best-effort: any "nothing to wrap" condition returns nil; only a genuine I/O
// failure mid-wrap returns an error.
func wrapEntrypoint(spec *oci.Spec, rootfs string) error {
	if spec.Process == nil {
		return nil
	}
	env := spec.Process.Env

	// 1. prefix = DEVSHELL_PREFIX env value; empty -> nothing to do.
	prefix := envValue(env, "DEVSHELL_PREFIX")
	if prefix == "" {
		return nil
	}

	// 2. entry = args[0]; empty -> nothing to do.
	if len(spec.Process.Args) == 0 {
		return nil
	}
	entry := spec.Process.Args[0]
	if entry == "" {
		return nil
	}

	// 3. imgPath = PATH env value (the image's PATH at hook time).
	imgPath := envValue(env, "PATH")

	// hostOf maps a container path to a host-accessible path. The hook runs in
	// the host ns where the container's bind-mounts are NOT visible: map a path
	// under a bind to its mount source (longest matching destination wins), else
	// fall back to the rootfs overlay (which IS visible in the host ns).
	hostOf := makeHostOf(spec.Mounts, rootfs)

	// wrapAt writes a PATH-prepending shim at the rootfs-overlay location for the
	// given container path (the shim always lands in the writable rootfs, never
	// on a RO mount), exec'ing target (itself a container path resolved inside
	// the container at runtime). Creates the parent dir, requires it writable,
	// chmod 0755.
	wrapAt := func(containerPath, target string) error {
		shimHost := filepath.Join(rootfs, containerPath)
		dir := filepath.Dir(shimHost)
		if !isDir(dir) {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("wrapAt: mkdir %s: %w", dir, err)
			}
		}
		if !isWritable(dir) {
			return fmt.Errorf("wrapAt: %s not writable", dir)
		}
		// Exactly the MVP's printf format (lines 43): the prefix and $PATH share
		// one pair of double quotes, and target is double-quoted. prefix/target
		// are container paths with no shell metacharacters, matching the MVP.
		content := fmt.Sprintf("#!/bin/sh\nexport PATH=\"%s:$PATH\"\nexec \"%s\" \"$@\"\n", prefix, target)
		if err := os.WriteFile(shimHost, []byte(content), 0o755); err != nil {
			return fmt.Errorf("wrapAt: write %s: %w", shimHost, err)
		}
		// WriteFile honours umask; force the exec bits explicitly.
		if err := os.Chmod(shimHost, 0o755); err != nil {
			return fmt.Errorf("wrapAt: chmod %s: %w", shimHost, err)
		}
		return nil
	}

	if strings.HasPrefix(entry, "/") {
		// Absolute entrypoint: crun execs it directly -> wrap in place.
		//
		// Only proceed if the path EXISTS in the rootfs overlay and its parent
		// is writable. This existence check is exactly the T9 limitation: an
		// absolute path into a RO-mounted prefix (e.g. /nix/store/.../bin/tool)
		// is not present in the rootfs overlay -> skip, leave intact, return nil.
		entryHost := filepath.Join(rootfs, entry)
		if !exists(entryHost) || !isWritable(filepath.Dir(entryHost)) {
			return nil
		}
		realHost := entryHost + ".real"
		if !exists(realHost) {
			if err := os.Rename(entryHost, realHost); err != nil {
				return fmt.Errorf("wrapEntrypoint: move %s aside: %w", entryHost, err)
			}
		}
		return wrapAt(entry, entry+".real")
	}

	// Relative entrypoint: resolve command-v-style across prefix dirs THEN
	// imgPath dirs. For each dir d, the candidate container path is d/entry; if
	// hostOf(candidate) exists and is executable on the host, that's the real
	// target (a container path). Take the first match.
	var real string
	for _, d := range append(splitPath(prefix), splitPath(imgPath)...) {
		candidate := d + "/" + entry
		if isExecutable(hostOf(candidate)) {
			real = candidate
			break
		}
	}
	if real == "" {
		return nil
	}

	// Shadow it with a shim in the FIRST image-PATH dir so crun finds it first.
	imgDirs := splitPath(imgPath)
	if len(imgDirs) == 0 {
		return nil
	}
	shimDir := imgDirs[0]
	shimPath := shimDir + "/" + entry
	if shimPath == real {
		// The shim would clobber the real binary (it lives in the first PATH
		// dir): move the real aside and exec the moved-aside copy instead.
		realHost := filepath.Join(rootfs, real)
		movedHost := realHost + ".real"
		if !exists(movedHost) {
			if err := os.Rename(realHost, movedHost); err != nil {
				return fmt.Errorf("wrapEntrypoint: move %s aside: %w", realHost, err)
			}
		}
		real = real + ".real"
	}
	return wrapAt(shimPath, real)
}

// makeHostOf builds a container-path -> host-path resolver for the given mounts
// and rootfs. The hook runs in the host namespace where the container's
// bind-mounts are NOT visible, so a path under a bind is remapped to its mount
// source (longest matching destination wins when mounts nest); an unmounted
// path falls back to the rootfs overlay, which IS visible in the host ns.
func makeHostOf(mounts []oci.Mount, rootfs string) func(string) string {
	return func(containerPath string) string {
		bestDest, bestSrc := "", ""
		found := false
		for _, m := range mounts {
			if m.Destination == "" {
				continue
			}
			if !pathHasPrefix(containerPath, m.Destination) {
				continue
			}
			if !found || len(m.Destination) > len(bestDest) {
				bestDest, bestSrc, found = m.Destination, m.Source, true
			}
		}
		if found {
			return bestSrc + strings.TrimPrefix(containerPath, bestDest)
		}
		return filepath.Join(rootfs, containerPath)
	}
}

// envValue returns the value of the first "<key>=<value>" entry in env, or "".
func envValue(env []string, key string) string {
	pfx := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, pfx) {
			return strings.TrimPrefix(e, pfx)
		}
	}
	return ""
}

// splitPath splits a colon-separated PATH-like string, dropping empty fields.
func splitPath(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ":") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// pathHasPrefix reports whether p is equal to prefix or lies beneath it on a
// path-separator boundary, so "/nix" matches "/nix/store/x" but not "/nixfoo".
func pathHasPrefix(p, prefix string) bool {
	if prefix == "/" {
		return strings.HasPrefix(p, "/")
	}
	if p == prefix {
		return true
	}
	return strings.HasPrefix(p, prefix+"/")
}

// exists reports whether path exists (file, dir, or symlink target).
func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// isDir reports whether path exists and is a directory.
func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// isExecutable reports whether path is a regular file with an executable bit
// set (mirrors the MVP's `[ -x ... ]` test on a host-mapped path).
func isExecutable(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false
	}
	return fi.Mode().Perm()&0o111 != 0
}

// isWritable reports whether the given directory is writable by the current
// process (mirrors the MVP's `[ -w "$dir" ]` guard before writing a shim).
func isWritable(dir string) bool {
	return unixAccessWritable(dir)
}
