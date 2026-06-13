// Package install registers the shared CDI spec directory (PLAN §2) with
// podman and docker so a shared-mode device reference
// (`--device nix-direnv.cdi/shell=<hash>`) resolves without a per-run
// `--cdi-spec-dir` flag. (PLAN §3 "install", milestone 7.)
//
// Strategy per runtime:
//   - podman: write a dedicated drop-in
//     `$XDG_CONFIG_HOME/containers/containers.conf.d/nix-direnv-cdi.conf`.
//     This is podman's idiomatic, non-destructive merge mechanism: we own the
//     whole file (no need to parse the user's hand-maintained containers.conf,
//     hence no TOML dependency), and the array carries the defaults so it is
//     correct whether podman replaces or appends on merge.
//   - docker: merge "cdi-spec-dirs" into /etc/docker/daemon.json (strict JSON,
//     stdlib only). Requires root and a daemon restart to take effect, so it is
//     attempted only when docker is detected.
//
// Both paths are backup-then-auto with a manual fallback: a pre-existing target
// is copied to "<path>.bak" before being rewritten, and if backup or write
// fails the exact change plus instructions are printed for the user to apply by
// hand. The shared dir itself is created 0755 for traversability (PLAN §1).
package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// defaultSpecDirs are the CDI spec dirs both runtimes scan out of the box. We
// carry them explicitly so registering our shared dir never drops them.
var defaultSpecDirs = []string{"/etc/cdi", "/var/run/cdi"}

// Paths are the filesystem targets install writes to. Factored out so tests can
// redirect them into a temp dir; DefaultPaths resolves the real locations.
type Paths struct {
	SharedDir        string // the shared CDI spec dir to register (created 0755)
	PodmanDropin     string // containers.conf.d/nix-direnv-cdi.conf
	DockerDaemonJSON string // /etc/docker/daemon.json
}

// DefaultPaths resolves the real install targets for sharedDir. The podman
// drop-in lives under $XDG_CONFIG_HOME/containers (or ~/.config/containers);
// docker's daemon config is the conventional /etc/docker/daemon.json.
func DefaultPaths(sharedDir string) (Paths, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Paths{}, fmt.Errorf("resolve home dir: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return Paths{
		SharedDir:        sharedDir,
		PodmanDropin:     filepath.Join(base, "containers", "containers.conf.d", "nix-direnv-cdi.conf"),
		DockerDaemonJSON: "/etc/docker/daemon.json",
	}, nil
}

// Run registers p.SharedDir with podman (always) and docker (if detected),
// writing a human-readable transcript to w. It returns an error only for
// catastrophic failures (e.g. the shared dir can't be created); a runtime that
// can't be auto-registered falls back to printed instructions, which is a
// successful outcome, not an error.
func Run(p Paths, w io.Writer) error {
	if err := os.MkdirAll(p.SharedDir, 0o755); err != nil {
		return fmt.Errorf("create shared CDI dir %s: %w", p.SharedDir, err)
	}
	fmt.Fprintf(w, "shared CDI spec dir: %s\n", p.SharedDir)

	// podman: user-level, always safe to attempt.
	if err := installPodman(p.PodmanDropin, p.SharedDir, w); err != nil {
		fmt.Fprintf(w, "podman: could not auto-register (%v); apply manually:\n", err)
		printPodmanManual(w, p.SharedDir, p.PodmanDropin)
	}

	// docker: root-level; only meaningful if docker is present.
	if dockerPresent() {
		if err := installDocker(p.DockerDaemonJSON, p.SharedDir, w); err != nil {
			fmt.Fprintf(w, "docker: could not auto-register (%v); apply manually:\n", err)
			printDockerManual(w, p.SharedDir, p.DockerDaemonJSON)
		}
	} else {
		fmt.Fprintln(w, "docker: not detected; if you use docker, apply manually:")
		printDockerManual(w, p.SharedDir, p.DockerDaemonJSON)
	}
	return nil
}

// installPodman writes the drop-in, backing up any pre-existing one first. It is
// idempotent: if the file already holds exactly our content, it is a no-op.
func installPodman(dropinPath, sharedDir string, w io.Writer) error {
	want := podmanDropinContent(sharedDir)
	if existing, err := os.ReadFile(dropinPath); err == nil {
		if string(existing) == want {
			fmt.Fprintf(w, "podman: already registered (%s)\n", dropinPath)
			return nil
		}
		bak, berr := backupFile(dropinPath)
		if berr != nil {
			return fmt.Errorf("backup %s: %w", dropinPath, berr)
		}
		fmt.Fprintf(w, "podman: backed up existing drop-in -> %s\n", bak)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dropinPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(dropinPath, []byte(want), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(w, "podman: registered %s\n  (wrote %s)\n", sharedDir, dropinPath)
	return nil
}

// installDocker merges the shared dir into daemon.json, backing up the original
// first. Idempotent if the dir is already listed.
func installDocker(daemonPath, sharedDir string, w io.Writer) error {
	var existing []byte
	if data, err := os.ReadFile(daemonPath); err == nil {
		existing = data
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	merged, changed, err := mergeDockerSpecDirs(existing, sharedDir)
	if err != nil {
		return err
	}
	if !changed {
		fmt.Fprintf(w, "docker: already registered (%s)\n", daemonPath)
		return nil
	}
	if len(existing) > 0 {
		bak, berr := backupFile(daemonPath)
		if berr != nil {
			return fmt.Errorf("backup %s: %w", daemonPath, berr)
		}
		fmt.Fprintf(w, "docker: backed up %s -> %s\n", daemonPath, bak)
	}
	if err := os.MkdirAll(filepath.Dir(daemonPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(daemonPath, merged, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(w, "docker: registered %s\n  (wrote %s; restart to apply: sudo systemctl restart docker)\n", sharedDir, daemonPath)
	return nil
}

// mergeDockerSpecDirs returns daemon.json content with sharedDir present in
// "cdi-spec-dirs", preserving all other keys. changed is false when sharedDir
// is already listed (so no backup/write is needed). It is the pure core of the
// docker path and is unit-tested directly. Absent input seeds the default dirs.
func mergeDockerSpecDirs(existing []byte, sharedDir string) (out []byte, changed bool, err error) {
	cfg := map[string]any{}
	if len(bytes.TrimSpace(existing)) > 0 {
		if err := json.Unmarshal(existing, &cfg); err != nil {
			return nil, false, fmt.Errorf("parse daemon.json: %w", err)
		}
	}

	var dirs []string
	switch v := cfg["cdi-spec-dirs"].(type) {
	case nil:
		dirs = append(dirs, defaultSpecDirs...)
	case []any:
		for _, e := range v {
			s, ok := e.(string)
			if !ok {
				return nil, false, fmt.Errorf("daemon.json: cdi-spec-dirs has a non-string entry %v", e)
			}
			dirs = append(dirs, s)
		}
	default:
		return nil, false, fmt.Errorf("daemon.json: cdi-spec-dirs is not an array")
	}

	for _, d := range dirs {
		if d == sharedDir {
			return nil, false, nil // already present
		}
	}
	dirs = append(dirs, sharedDir)
	cfg["cdi-spec-dirs"] = dirs

	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, false, err
	}
	return append(b, '\n'), true, nil
}

// podmanDropinContent is the full, canonical text of our containers.conf.d
// drop-in for sharedDir. The defaults are listed so the result is correct
// whether podman replaces or appends the array on merge.
func podmanDropinContent(sharedDir string) string {
	var b strings.Builder
	b.WriteString("# Managed by nix-direnv-cdi (`nix-direnv-cdi install`).\n")
	b.WriteString("# Registers the shared CDI spec dir so a shared-mode device\n")
	b.WriteString("# resolves without `--cdi-spec-dir`. Safe to delete to unregister.\n")
	b.WriteString("[engine]\n")
	b.WriteString("cdi_spec_dirs = [\n")
	for _, d := range append(append([]string{}, defaultSpecDirs...), sharedDir) {
		fmt.Fprintf(&b, "  %q,\n", d)
	}
	b.WriteString("]\n")
	return b.String()
}

// backupFile copies path to "path.bak", preserving its mode. It is only called
// when path is known to exist.
func backupFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	mode := fs.FileMode(0o644)
	if fi, serr := os.Stat(path); serr == nil {
		mode = fi.Mode().Perm()
	}
	bak := path + ".bak"
	if err := os.WriteFile(bak, data, mode); err != nil {
		return "", err
	}
	return bak, nil
}

// dockerPresent reports whether docker is worth configuring: a docker binary on
// PATH or an existing /etc/docker dir.
func dockerPresent() bool {
	if _, err := exec.LookPath("docker"); err == nil {
		return true
	}
	if fi, err := os.Stat("/etc/docker"); err == nil && fi.IsDir() {
		return true
	}
	return false
}

func printPodmanManual(w io.Writer, sharedDir, dropin string) {
	fmt.Fprintf(w, "  create %s with:\n\n%s\n", dropin, indent(podmanDropinContent(sharedDir)))
}

func printDockerManual(w io.Writer, sharedDir, daemonPath string) {
	snippet, _, _ := mergeDockerSpecDirs(nil, sharedDir)
	fmt.Fprintf(w, "  add %q to \"cdi-spec-dirs\" in %s (create it if absent), e.g.:\n\n%s\n", sharedDir, daemonPath, indent(string(snippet)))
	fmt.Fprintln(w, "  then: sudo systemctl restart docker")
	fmt.Fprintln(w, "  (docker <28.3 also needs \"features\": { \"cdi\": true } in the same file.)")
}

// indent prefixes every non-empty line with four spaces for readable snippets.
func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, ln := range lines {
		if ln != "" {
			lines[i] = "    " + ln
		}
	}
	return strings.Join(lines, "\n") + "\n"
}
