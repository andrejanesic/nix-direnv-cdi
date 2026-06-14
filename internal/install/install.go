// Package install registers the generic CDI spec with podman and docker so the
// device reference (`--device nix-direnv-cdi.org/env=current`) resolves without
// a per-run `--cdi-spec-dir` flag.
//
// Strategy per runtime:
//   - podman: write a dedicated drop-in
//     `$XDG_CONFIG_HOME/containers/containers.conf.d/nix-direnv-cdi.conf`.
//     This is podman's idiomatic, non-destructive merge mechanism: we own the
//     whole file (no need to parse the user's hand-maintained containers.conf,
//     hence no TOML dependency), and the array carries the defaults so it is
//     correct whether podman replaces or appends on merge.
//   - docker: write the same generic spec to Docker's daemon-scanned system CDI
//     directory, /etc/cdi/nix-direnv.json. Docker is system-wide, so normal
//     install does not add a per-user CDI dir to /etc/docker/daemon.json.
//
// Both paths are backup-then-auto with a manual fallback: a pre-existing target
// is copied to "<path>.bak" before being rewritten, and if backup or write
// fails the exact change plus instructions are printed for the user to apply by
// hand. The shared dir itself is created 0755 for traversability.
package install

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/andrejanesic/nix-direnv-cdi/internal/cdispec"
)

// defaultSpecDirs are the CDI spec dirs both runtimes scan out of the box. We
// carry them explicitly so registering our shared dir never drops them.
var defaultSpecDirs = []string{"/etc/cdi", "/var/run/cdi"}

// Paths are the filesystem targets install writes to. Factored out so tests can
// redirect them into a temp dir; DefaultPaths resolves the real locations.
type Paths struct {
	SharedDir      string // the user shared CDI spec dir for podman (created 0755)
	PodmanDropin   string // containers.conf.d/nix-direnv-cdi.conf
	DockerSpecPath string // /etc/cdi/nix-direnv.json
}

// DefaultPaths resolves the real install targets for sharedDir. The podman
// drop-in lives under $XDG_CONFIG_HOME/containers (or ~/.config/containers);
// docker uses the system CDI spec path scanned by the daemon.
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
		SharedDir:      sharedDir,
		PodmanDropin:   filepath.Join(base, "containers", "containers.conf.d", "nix-direnv-cdi.conf"),
		DockerSpecPath: filepath.Join("/etc/cdi", cdispec.FileName),
	}, nil
}

// Run registers p.SharedDir with podman (always) and installs specData for
// docker (if detected), writing a human-readable transcript to w. It returns an
// error only for catastrophic failures (e.g. the shared dir can't be created);
// a runtime that can't be auto-registered falls back to printed instructions,
// which is a successful outcome, not an error.
func Run(p Paths, specData []byte, w io.Writer) error {
	if err := os.MkdirAll(p.SharedDir, 0o755); err != nil {
		return fmt.Errorf("create shared CDI dir %s: %w", p.SharedDir, err)
	}
	fmt.Fprintf(w, "shared CDI spec dir: %s\n", p.SharedDir)

	// podman: user-level, always safe to attempt.
	if err := installPodman(p.PodmanDropin, p.SharedDir, w); err != nil {
		fmt.Fprintf(w, "podman: could not auto-register (%v); apply manually:\n", err)
		printPodmanManual(w, p.SharedDir, p.PodmanDropin)
	}

	// docker: system-daemon path; only meaningful if docker is present.
	if dockerPresent() {
		if err := installDockerSpec(p.DockerSpecPath, specData, w); err != nil {
			fmt.Fprintf(w, "docker: could not install system CDI spec (%v); apply manually:\n", err)
			printDockerManual(w, filepath.Join(p.SharedDir, cdispec.FileName), p.DockerSpecPath)
		}
	} else {
		fmt.Fprintln(w, "docker: not detected; if you use docker, apply manually:")
		printDockerManual(w, filepath.Join(p.SharedDir, cdispec.FileName), p.DockerSpecPath)
	}
	return nil
}

// Uninstall removes this tool's owned CDI registration artifacts. It is
// idempotent: missing files are reported as already absent. Docker's system spec
// is removed only when docker is detected or the spec file already exists.
func Uninstall(p Paths, w io.Writer) error {
	fmt.Fprintf(w, "shared CDI spec dir: %s\n", p.SharedDir)

	specPath := filepath.Join(p.SharedDir, cdispec.FileName)
	if err := uninstallSpec(specPath, w); err != nil {
		return err
	}

	if err := uninstallPodman(p.PodmanDropin, w); err != nil {
		fmt.Fprintf(w, "podman: could not auto-unregister (%v); remove manually:\n", err)
		printPodmanUninstallManual(w, p.PodmanDropin)
	}

	if dockerPresent() || fileExists(p.DockerSpecPath) {
		if err := uninstallDockerSpec(p.DockerSpecPath, w); err != nil {
			fmt.Fprintf(w, "docker: could not remove system CDI spec (%v); remove manually:\n", err)
			printDockerUninstallManual(w, p.DockerSpecPath)
		}
	} else {
		fmt.Fprintln(w, "docker: not detected and system CDI spec absent; no-op")
	}
	return nil
}

func uninstallSpec(specPath string, w io.Writer) error {
	if err := os.Remove(specPath); err == nil {
		fmt.Fprintf(w, "cdi: removed %s\n", specPath)
		return nil
	} else if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(w, "cdi: already absent (%s)\n", specPath)
		return nil
	} else {
		return fmt.Errorf("remove %s: %w", specPath, err)
	}
}

func uninstallPodman(dropinPath string, w io.Writer) error {
	if err := os.Remove(dropinPath); err == nil {
		fmt.Fprintf(w, "podman: removed %s\n", dropinPath)
		return nil
	} else if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(w, "podman: already absent (%s)\n", dropinPath)
		return nil
	} else {
		return err
	}
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

// installDockerSpec writes the generic spec to Docker's system CDI path,
// backing up any divergent pre-existing file first. It is idempotent when the
// file already holds exactly specData.
func installDockerSpec(specPath string, specData []byte, w io.Writer) error {
	if existing, err := os.ReadFile(specPath); err == nil {
		if bytes.Equal(existing, specData) {
			fmt.Fprintf(w, "docker: already installed (%s)\n", specPath)
			return nil
		}
		bak, berr := backupFile(specPath)
		if berr != nil {
			return fmt.Errorf("backup %s: %w", specPath, berr)
		}
		fmt.Fprintf(w, "docker: backed up existing system CDI spec -> %s\n", bak)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(specPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(specPath, specData, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(w, "docker: installed system CDI spec\n  (wrote %s)\n", specPath)
	return nil
}

func uninstallDockerSpec(specPath string, w io.Writer) error {
	if err := os.Remove(specPath); err == nil {
		fmt.Fprintf(w, "docker: removed system CDI spec %s\n", specPath)
		return nil
	} else if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(w, "docker: system CDI spec already absent (%s)\n", specPath)
		return nil
	} else {
		return err
	}
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

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func printPodmanManual(w io.Writer, sharedDir, dropin string) {
	fmt.Fprintf(w, "  create %s with:\n\n%s\n", dropin, indent(podmanDropinContent(sharedDir)))
}

func printDockerManual(w io.Writer, userSpecPath, dockerSpecPath string) {
	fmt.Fprintf(w, "  install the generated generic CDI spec from:\n    %s\n  to Docker's system CDI path:\n    %s\n", userSpecPath, dockerSpecPath)
	fmt.Fprintln(w, "  for example:")
	fmt.Fprintf(w, "    sudo install -D -m 0644 %s %s\n", userSpecPath, dockerSpecPath)
	fmt.Fprintln(w, "  Docker scans this system CDI directory; do not add a per-user CDI dir to /etc/docker/daemon.json on multi-user hosts.")
	fmt.Fprintln(w, "  (docker <28.3 may also need the CDI feature enabled in daemon.json.)")
}

func printPodmanUninstallManual(w io.Writer, dropin string) {
	fmt.Fprintf(w, "  remove %s\n", dropin)
}

func printDockerUninstallManual(w io.Writer, dockerSpecPath string) {
	fmt.Fprintf(w, "  remove %s\n", dockerSpecPath)
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
