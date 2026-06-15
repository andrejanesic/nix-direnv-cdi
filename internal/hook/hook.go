// Package hook implements the createRuntime OCI hook. The hook runs in the host
// namespace after the container's mount namespace and mounts exist; from there
// it (1) bind-mounts the project's dev-shell closure into the container by
// entering the container's mount namespace, and (2) wraps the entrypoint so the
// dev-shell prefix is prepended to PATH additively and the dev-shell env vars
// are exported. It reads the loaded-direnv environment (DIRENV_DIR,
// DIRENV_DIFF) from the hook environment when available, falling back to the OCI
// process environment for daemon-driven CLIs. See docs/mechanisms.md.
//
// The entrypoint-wrap algorithm and its T1-T10 behaviour matrix (incl. the T9
// limitation) are documented in docs/mechanisms.md; the dynamic mount injection
// is the dynamic-design addition.
package hook

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	oci "github.com/opencontainers/runtime-spec/specs-go"

	"github.com/andrejanesic/nix-direnv-cdi/internal/devshell"
	"github.com/andrejanesic/nix-direnv-cdi/internal/nsmount"
	"github.com/andrejanesic/nix-direnv-cdi/internal/ociconfig"
)

// mountFunc injects the closure into the container's mount namespace. Injected
// so the testable core (run) can be exercised without a real container; the
// production implementation is nsmount.BindAll.
type mountFunc func(pid int, rootfs string, closure []string) error

// getenvFunc mirrors os.LookupEnv (the hook's inherited environment).
type getenvFunc func(string) (string, bool)

// Run executes the createRuntime hook: read the OCI State from stdin, load
// <bundle>/config.json, resolve the rootfs, then run the core. Best-effort: the
// caller ignores the returned error and exits 0 so the container is never
// broken.
func Run(in io.Reader) error {
	state, err := ociconfig.ReadState(in)
	if err != nil {
		return err
	}
	spec, err := ociconfig.Load(state.Bundle)
	if err != nil {
		return err
	}
	rootfs := resolveRootfs(spec, state.Bundle)
	// Diagnostic: log as early as possible via both env channels (ambient +
	// container config.json). If this line appears but run()'s do not, the fault
	// is between here and the gate; if it never appears, NDC_HOOK_LOG reached the
	// hook through neither channel.
	debugLog(getenvWithProcessEnv(os.LookupEnv, spec))("Run: pid=%d bundle=%s rootfs=%q", state.Pid, state.Bundle, rootfs)
	if rootfs == "" {
		return nil
	}
	return run(state, spec, rootfs, os.LookupEnv, nsmount.BindAll)
}

// resolveRootfs returns the absolute rootfs path from config.json's root.path
// (joined onto the bundle when relative), or "" if absent.
func resolveRootfs(spec *oci.Spec, bundle string) string {
	if spec.Root == nil || spec.Root.Path == "" {
		return ""
	}
	rootfs := spec.Root.Path
	if !filepath.IsAbs(rootfs) {
		rootfs = filepath.Join(bundle, rootfs)
	}
	return rootfs
}

// run is the testable core (no real container). It gates on DIRENV_DIR (being
// in a loaded dev-shell is the authorization), then injects the closure mounts
// and wraps the entrypoint for additive PATH + dev-shell env. Best-effort: a
// mount failure is logged but never blocks the wrap or breaks the container.
func run(state *oci.State, spec *oci.Spec, rootfs string, getenv getenvFunc, mount mountFunc) (rerr error) {
	runtimeGetenv := getenvWithProcessEnv(getenv, spec)
	dbg := debugLog(runtimeGetenv)

	// A createRuntime hook must never break the container, so capture any panic
	// into the debug log (the only window crun leaves open) and surface it as a
	// returned error the caller already treats as best-effort.
	defer func() {
		if r := recover(); r != nil {
			dbg("PANIC: %v", r)
			rerr = fmt.Errorf("hook panic: %v", r)
		}
	}()

	dirRaw, ok := runtimeGetenv("DIRENV_DIR")
	if !ok || dirRaw == "" {
		dbg("gate closed: DIRENV_DIR unset; device inert")
		return nil // not in an approved dev-shell -> the device is inert
	}
	project := strings.TrimPrefix(dirRaw, "-") // direnv's leading '-' marker
	dbg("gate open: project=%s pid=%d rootfs=%s", project, state.Pid, rootfs)

	// 1. Inject the project's closure mounts (best-effort). The allowlist root
	// is /nix/store, overridable via NIX_STORE_DIR for a relocated store.
	storeDir := devshell.DefaultStoreDir
	if v, ok := runtimeGetenv("NIX_STORE_DIR"); ok && v != "" {
		storeDir = v
	}
	closure, rerr := devshell.ReadMounts(filepath.Join(project, ".direnv", "cdi", "mounts.json"), storeDir)
	dbg("mounts.json: %d paths, readErr=%v", len(closure), rerr)
	if rerr == nil && len(closure) > 0 && state.Pid > 0 {
		if merr := mount(state.Pid, rootfs, closure); merr != nil {
			dbg("mount FAILED: %v", merr)
			fmt.Fprintln(os.Stderr, "nix-direnv-cdi hook: mount (ignored):", merr)
		} else {
			dbg("mount OK: %d paths bound under %s", len(closure), rootfs)
		}
	}

	// 2. Additive PATH + dev-shell env via entrypoint wrapping.
	prefix, env, has, derr := devshell.RuntimeEnv(runtimeGetenv)
	dbg("runtimeEnv: prefix=%d env=%d has=%v err=%v", len(prefix), len(env), has, derr)
	if derr != nil {
		return derr
	}
	if !has {
		return nil
	}
	return wrapEntrypoint(spec, rootfs, prefix, env)
}

func getenvWithProcessEnv(getenv getenvFunc, spec *oci.Spec) getenvFunc {
	return func(key string) (string, bool) {
		if v, ok := getenv(key); ok {
			return v, true
		}
		if spec == nil || spec.Process == nil {
			return "", false
		}
		return lookupEnv(spec.Process.Env, key)
	}
}

// debugLog returns a logger that appends to the file named by NDC_HOOK_LOG, or
// a no-op when it is unset. A createRuntime hook is best-effort and silent, so
// this is the only way to diagnose it.
func debugLog(getenv getenvFunc) func(string, ...any) {
	path, ok := getenv("NDC_HOOK_LOG")
	if !ok || path == "" {
		return func(string, ...any) {}
	}
	return func(format string, args ...any) {
		// 0600 so the log (project paths, closure sizes, env-var names) isn't
		// world-readable, and O_NOFOLLOW so a pre-planted symlink at the path
		// can't redirect the write. NDC_HOOK_LOG is a debug-only knob; point it
		// at a private path.
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0o600)
		if err != nil {
			return
		}
		defer f.Close()
		fmt.Fprintf(f, "nix-direnv-cdi hook: "+format+"\n", args...)
	}
}

// wrapEntrypoint writes a shim that prepends prefix to PATH and exports the
// dev-shell env vars before exec'ing the real entrypoint. It mirrors the MVP
// case-split:
//
//   - absolute entry -> crun execs it directly; wrap in place inside the
//     writable rootfs. An absolute path NOT present in the rootfs (e.g. into the
//     RO-mounted /nix/store closure we injected) is left untouched -> the
//     documented T9 limitation.
//   - relative entry -> resolve command-v-style across prefix (host-accessible
//     /nix paths, identity) then the image PATH (inside the rootfs), then shadow
//     it with a shim in the first image-PATH dir so crun finds it first.
//
// Best-effort: any "nothing to wrap" condition returns nil; only a genuine I/O
// failure mid-wrap returns an error.
func wrapEntrypoint(spec *oci.Spec, rootfs string, prefix []string, env map[string]string) error {
	if spec.Process == nil {
		return nil
	}
	if len(prefix) == 0 && len(env) == 0 {
		return nil
	}
	if len(spec.Process.Args) == 0 {
		return nil
	}
	entry := spec.Process.Args[0]
	if entry == "" {
		return nil
	}
	imgPath := envValue(spec.Process.Env, "PATH")

	// The export block injected into every shim: additive PATH (prefix entries
	// are /nix store paths with no shell metacharacters, so the MVP's
	// double-quoted form is safe) plus each dev-shell var single-quoted (values
	// may contain spaces, quotes, even newlines).
	var blk strings.Builder
	blk.WriteString("unset DIRENV_DIR DIRENV_DIFF\n")
	if len(prefix) > 0 {
		fmt.Fprintf(&blk, "export PATH=\"%s:$PATH\"\n", strings.Join(prefix, ":"))
	}
	for _, k := range sortedKeys(env) {
		if !validEnvName(k) {
			// The name is emitted unquoted in `export <name>=...`; a name with
			// shell metacharacters would inject into the shim. Skip it rather
			// than risk executing it inside the container.
			continue
		}
		fmt.Fprintf(&blk, "export %s=%s\n", k, shellQuote(env[k]))
	}
	exports := blk.String()

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
		content := "#!/bin/sh\n" + exports + fmt.Sprintf("exec \"%s\" \"$@\"\n", target)
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
		// Absolute entrypoint: wrap in place only if present in the writable
		// rootfs. A /nix/store path (our RO injected closure) is not present in
		// the host-ns view of the rootfs -> skip = the T9 limitation.
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

	// Relative entrypoint. Resolve across prefix (host-accessible /nix paths:
	// the candidate IS its own host path) then the image PATH (inside the
	// rootfs). real is the resolved CONTAINER path the shim will exec.
	var real string
	for _, d := range prefix {
		cand := d + "/" + entry
		if isExecutable(cand) { // d is a host /nix store path
			real = cand
			break
		}
	}
	if real == "" {
		for _, d := range splitPath(imgPath) {
			cand := d + "/" + entry
			if isExecutable(filepath.Join(rootfs, cand)) { // image path inside the rootfs
				real = cand
				break
			}
		}
	}
	if real == "" {
		return nil
	}

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

// validEnvName reports whether k is a POSIX-shell-safe environment variable
// name (^[A-Za-z_][A-Za-z0-9_]*$). The shim emits `export <name>=...` with the
// name unquoted, so a name carrying shell metacharacters would otherwise inject
// into the container's entrypoint; such names are skipped.
func validEnvName(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		switch {
		case r == '_':
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// shellQuote single-quotes s for safe inclusion in a /bin/sh script, escaping
// embedded single quotes. Single-quoted strings preserve every other byte
// verbatim (including newlines), which dev-shell values can contain.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// sortedKeys returns m's keys sorted, for deterministic shim output.
func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// envValue returns the value of the first "<key>=<value>" entry in env, or "".
func envValue(env []string, key string) string {
	v, _ := lookupEnv(env, key)
	return v
}

func lookupEnv(env []string, key string) (string, bool) {
	pfx := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, pfx) {
			return strings.TrimPrefix(e, pfx), true
		}
	}
	return "", false
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
