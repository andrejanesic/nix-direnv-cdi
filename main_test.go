package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrejanesic/nix-direnv-cdi/internal/cdispec"
)

func TestFormatVersion_Default(t *testing.T) {
	oldVersion, oldCommit, oldBuildDate := version, commit, buildDate
	t.Cleanup(func() {
		version, commit, buildDate = oldVersion, oldCommit, oldBuildDate
	})

	version, commit, buildDate = "dev", "unknown", "unknown"
	if got, want := formatVersion(), "nix-direnv-cdi dev"; got != want {
		t.Fatalf("formatVersion() = %q, want %q", got, want)
	}
}

func TestFormatVersion_ReleaseMetadata(t *testing.T) {
	oldVersion, oldCommit, oldBuildDate := version, commit, buildDate
	t.Cleanup(func() {
		version, commit, buildDate = oldVersion, oldCommit, oldBuildDate
	})

	version, commit, buildDate = "v0.1.0", "abc1234", "20260615"
	if got, want := formatVersion(), "nix-direnv-cdi v0.1.0 (commit abc1234, built 20260615)"; got != want {
		t.Fatalf("formatVersion() = %q, want %q", got, want)
	}
}

func TestCLIInstallUninstallSmoke(t *testing.T) {
	if dockerHostConfigExistsForSmoke() {
		t.Skip("host /etc/docker exists; CLI install has no flag to redirect Docker system CDI spec safely")
	}
	if dockerSystemSpecExistsForSmoke() {
		t.Skip("host /etc/cdi/nix-direnv.json exists; CLI uninstall has no flag to redirect Docker system CDI spec safely")
	}

	dir := t.TempDir()
	bin := filepath.Join(dir, "nix-direnv-cdi")
	if out, err := exec.Command("go", "build", "-buildvcs=false", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	xdg := filepath.Join(dir, "xdg")
	emptyPath := filepath.Join(dir, "empty-path")
	if err := os.MkdirAll(emptyPath, 0o755); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(),
		"XDG_CONFIG_HOME="+xdg,
		"HOME="+filepath.Join(dir, "home"),
		"PATH="+emptyPath,
	)

	runCLI := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %s: %v\n%s", bin, strings.Join(args, " "), err, out)
		}
		return string(out)
	}

	specPath := filepath.Join(xdg, "cdi", cdispec.FileName)
	dropin := filepath.Join(xdg, "containers", "containers.conf.d", "nix-direnv-cdi.conf")

	out := runCLI("install")
	if !strings.Contains(out, "wrote generic CDI device") {
		t.Fatalf("install output missing spec write: %s", out)
	}
	if data, err := os.ReadFile(specPath); err != nil || !strings.Contains(string(data), cdispec.Kind) {
		t.Fatalf("spec missing or wrong: err=%v content=%q", err, data)
	}
	if data, err := os.ReadFile(dropin); err != nil || !strings.Contains(string(data), filepath.Join(xdg, "cdi")) {
		t.Fatalf("podman drop-in missing or wrong: err=%v content=%q", err, data)
	}

	out = runCLI("install")
	if !strings.Contains(out, "podman: already registered") {
		t.Fatalf("second install should be idempotent, got: %s", out)
	}
	if _, err := os.Stat(dropin + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("idempotent install should not create drop-in backup, stat err=%v", err)
	}

	out = runCLI("uninstall")
	if !strings.Contains(out, "cdi: removed") || !strings.Contains(out, "podman: removed") {
		t.Fatalf("uninstall output missing removals: %s", out)
	}
	for _, p := range []string{specPath, dropin} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s still exists or unexpected stat error: %v", p, err)
		}
	}

	out = runCLI("uninstall")
	if !strings.Contains(out, "cdi: already absent") || !strings.Contains(out, "podman: already absent") {
		t.Fatalf("second uninstall should be idempotent, got: %s", out)
	}
}

func dockerHostConfigExistsForSmoke() bool {
	fi, err := os.Stat("/etc/docker")
	return err == nil && fi.IsDir()
}

func dockerSystemSpecExistsForSmoke() bool {
	_, err := os.Stat(filepath.Join("/etc/cdi", cdispec.FileName))
	return err == nil
}
