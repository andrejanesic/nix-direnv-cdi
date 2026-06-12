// Package devshell discovers a nix-direnv dev-shell from the loaded direnv
// environment and the .direnv gcroot: the additive PATH prefix, the exported
// environment (minus PATH), and the full nix store closure to mount.
package devshell

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// DevShell is a discovered nix-direnv dev-shell.
type DevShell struct {
	// ProjectRoot is the project/workdir, from ${DIRENV_DIR#-} (fallback $PWD).
	ProjectRoot string
	// Prefix is the set of nix-store bin dirs that form the additive PATH
	// prefix (colon-joined into DEVSHELL_PREFIX).
	Prefix []string
	// Env holds the exported dev-shell variables, excluding PATH.
	Env map[string]string
	// Closure is every store path from `nix-store -qR` over the gcroot; each is
	// mounted read-only into the container.
	Closure []string
}

// getenvFunc looks up an environment variable, mirroring os.LookupEnv.
type getenvFunc func(key string) (string, bool)

// listClosureFunc returns the runtime closure (every required store path) of the
// given gcroot. The real implementation shells out to `nix-store -qR`.
type listClosureFunc func(gcroot string) ([]string, error)

// Discover is the real-world entry point: it reads os.Environ (the loaded
// direnv environment), resolves the gcroot from .direnv/flake-profile-*, and
// walks the closure via `nix-store -qR`. It delegates to the testable
// discover() so the unit tests can drive it with fakes (no nix, no direnv).
// (PLAN §3, milestone 2.)
func Discover() (*DevShell, error) {
	return discover(os.LookupEnv, os.Getwd, resolveGCRoot, listClosureNixStore)
}

// discover is the testable core. It takes the environment lookup, a working
// directory resolver, a gcroot resolver, and a closure lister as injectable
// seams so a fake environment and a fake closure can drive it.
func discover(
	getenv getenvFunc,
	getwd func() (string, error),
	resolveGCRootFn func(projectRoot string, getenv getenvFunc) (string, error),
	listClosure listClosureFunc,
) (*DevShell, error) {
	root, err := projectRoot(getenv, getwd)
	if err != nil {
		return nil, err
	}

	diff, ok := getenv("DIRENV_DIFF")
	if !ok || diff == "" {
		return nil, fmt.Errorf("DIRENV_DIFF is not set: no nix-direnv dev-shell loaded for %s", root)
	}
	prev, next, err := decodeDirenvDiff(diff)
	if err != nil {
		return nil, fmt.Errorf("decode DIRENV_DIFF: %w", err)
	}

	ds := &DevShell{
		ProjectRoot: root,
		Prefix:      prefixFromDiff(prev["PATH"], next["PATH"]),
		Env:         envFromDiff(next),
	}

	gcroot, err := resolveGCRootFn(root, getenv)
	if err != nil {
		return nil, err
	}
	closure, err := listClosure(gcroot)
	if err != nil {
		return nil, fmt.Errorf("list closure of %s: %w", gcroot, err)
	}
	ds.Closure = closure

	return ds, nil
}

// projectRoot derives the project root from DIRENV_DIR with a single leading
// '-' stripped (direnv's bookkeeping marker), falling back to the working dir.
func projectRoot(getenv getenvFunc, getwd func() (string, error)) (string, error) {
	if dir, ok := getenv("DIRENV_DIR"); ok && dir != "" {
		return strings.TrimPrefix(dir, "-"), nil
	}
	wd, err := getwd()
	if err != nil {
		return "", fmt.Errorf("resolve project root: %w", err)
	}
	return wd, nil
}

// decodeDirenvDiff decodes direnv's DIRENV_DIFF: base64 (raw URL encoding) of a
// zlib-compressed JSON object {"p":{prev},"n":{new}}. "p" holds the values that
// existed before direnv loaded (the keys it changed/removed); "n" holds the new
// values direnv exported. Returns (prev, next).
func decodeDirenvDiff(s string) (prev, next map[string]string, err error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, nil, fmt.Errorf("base64: %w", err)
	}
	zr, err := zlib.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, nil, fmt.Errorf("zlib: %w", err)
	}
	defer zr.Close()
	jsonBytes, err := io.ReadAll(zr)
	if err != nil {
		return nil, nil, fmt.Errorf("inflate: %w", err)
	}
	var diff struct {
		Prev map[string]string `json:"p"`
		Next map[string]string `json:"n"`
	}
	if err := json.Unmarshal(jsonBytes, &diff); err != nil {
		return nil, nil, fmt.Errorf("json: %w", err)
	}
	if diff.Prev == nil {
		diff.Prev = map[string]string{}
	}
	if diff.Next == nil {
		diff.Next = map[string]string{}
	}
	return diff.Prev, diff.Next, nil
}

// prefixFromDiff computes the additive PATH prefix: the entries present in the
// dev-shell's new PATH but absent from the PATH that existed before direnv
// loaded. These are exactly the dirs direnv prepended (the nix-store bin dirs of
// the dev-shell packages, plus .direnv/bin). Order is preserved.
func prefixFromDiff(prevPath, newPath string) []string {
	before := map[string]bool{}
	for _, d := range splitPath(prevPath) {
		before[d] = true
	}
	var prefix []string
	seen := map[string]bool{}
	for _, d := range splitPath(newPath) {
		if d == "" || before[d] || seen[d] {
			continue
		}
		seen[d] = true
		prefix = append(prefix, d)
	}
	return prefix
}

func splitPath(p string) []string {
	if p == "" {
		return nil
	}
	return strings.Split(p, ":")
}

// envFromDiff selects the exported dev-shell variables: every key direnv newly
// set ("n"), excluding PATH (the hook makes it additive) and direnv's own
// DIRENV_* bookkeeping.
func envFromDiff(next map[string]string) map[string]string {
	env := make(map[string]string, len(next))
	for k, v := range next {
		if k == "PATH" || strings.HasPrefix(k, "DIRENV_") {
			continue
		}
		env[k] = v
	}
	return env
}

// resolveGCRoot finds the nix-direnv flake-profile gcroot symlink under
// <root>/.direnv/flake-profile-* and resolves it to its /nix/store target.
func resolveGCRoot(projectRoot string, _ getenvFunc) (string, error) {
	pattern := filepath.Join(projectRoot, ".direnv", "flake-profile-*")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return "", fmt.Errorf("glob %s: %w", pattern, err)
	}
	// Exclude the companion *.rc files; keep only the gcroot symlinks.
	var roots []string
	for _, m := range matches {
		if strings.HasSuffix(m, ".rc") {
			continue
		}
		roots = append(roots, m)
	}
	if len(roots) == 0 {
		return "", fmt.Errorf("no nix-direnv gcroot found at %s (is the dev-shell materialized?)", pattern)
	}
	// Deterministic if several profiles exist: take the lexicographically last.
	sort.Strings(roots)
	gcroot := roots[len(roots)-1]
	target, err := filepath.EvalSymlinks(gcroot)
	if err != nil {
		return "", fmt.Errorf("resolve gcroot %s: %w", gcroot, err)
	}
	return target, nil
}

// listClosureNixStore returns the runtime closure of gcroot via `nix-store -qR`.
func listClosureNixStore(gcroot string) ([]string, error) {
	cmd := exec.Command("nix-store", "-qR", gcroot)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("nix-store -qR: %w: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("nix-store -qR: %w", err)
	}
	var paths []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			paths = append(paths, line)
		}
	}
	return paths, nil
}
