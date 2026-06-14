package install

// Unit tests for the install logic: podman drop-in content, Docker system CDI
// spec file management, and I/O orchestration against injected temp paths. No
// real podman/docker, no root: these run under -short.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrejanesic/nix-direnv-cdi/internal/cdispec"
)

const shared = "/home/u/.config/cdi"

var dockerSpecData = []byte(`{"kind":"nix-direnv-cdi.org/env","devices":[{"name":"current"}]}` + "\n")

func TestPodmanDropinContent(t *testing.T) {
	got := podmanDropinContent(shared)
	for _, want := range []string{"[engine]", "cdi_spec_dirs = [", `"/etc/cdi",`, `"/var/run/cdi",`, `"` + shared + `",`} {
		if !strings.Contains(got, want) {
			t.Errorf("drop-in missing %q\n---\n%s", want, got)
		}
	}
}

func TestUninstallPodman_Idempotent(t *testing.T) {
	dropin := filepath.Join(t.TempDir(), "containers", "containers.conf.d", "nix-direnv-cdi.conf")
	if err := os.MkdirAll(filepath.Dir(dropin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dropin, []byte("owned\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := uninstallPodman(dropin, &buf); err != nil {
		t.Fatalf("first uninstall: %v", err)
	}
	if _, err := os.Stat(dropin); !os.IsNotExist(err) {
		t.Fatalf("drop-in still exists or unexpected stat error: %v", err)
	}

	buf.Reset()
	if err := uninstallPodman(dropin, &buf); err != nil {
		t.Fatalf("second uninstall: %v", err)
	}
	if !strings.Contains(buf.String(), "already absent") {
		t.Errorf("second run should be a no-op, got: %s", buf.String())
	}
}

func TestUninstall_RemovesOwnedFilesAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "cdi")
	dropin := filepath.Join(dir, "containers", "containers.conf.d", "nix-direnv-cdi.conf")
	dockerSpec := filepath.Join(dir, "etc", "cdi", cdispec.FileName)
	spec := filepath.Join(sharedDir, cdispec.FileName)
	for _, p := range []string{sharedDir, filepath.Dir(dropin), filepath.Dir(dockerSpec)} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(spec, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dropin, []byte("owned\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dockerSpec, dockerSpecData, 0o644); err != nil {
		t.Fatal(err)
	}

	paths := Paths{
		SharedDir:      sharedDir,
		PodmanDropin:   dropin,
		DockerSpecPath: dockerSpec,
	}
	var buf bytes.Buffer
	if err := Uninstall(paths, &buf); err != nil {
		t.Fatalf("first uninstall: %v", err)
	}
	for _, p := range []string{spec, dropin, dockerSpec} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s still exists or unexpected stat error: %v", p, err)
		}
	}
	if _, err := os.Stat(sharedDir); err != nil {
		t.Fatalf("shared dir should be left in place: %v", err)
	}

	buf.Reset()
	if err := Uninstall(paths, &buf); err != nil {
		t.Fatalf("second uninstall: %v", err)
	}
	if got := buf.String(); !strings.Contains(got, "cdi: already absent") || !strings.Contains(got, "podman: already absent") {
		t.Errorf("second run should report no-ops, got: %s", got)
	}
}

func TestRunCreatesSharedDirAndPodmanDropinThenUninstallIsIdempotent(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dockerDetected := dockerConfigDirExists()

	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "cdi")
	dropin := filepath.Join(dir, "containers", "containers.conf.d", "nix-direnv-cdi.conf")
	dockerSpec := filepath.Join(dir, "etc", "cdi", cdispec.FileName)
	paths := Paths{
		SharedDir:      sharedDir,
		PodmanDropin:   dropin,
		DockerSpecPath: dockerSpec,
	}

	var buf bytes.Buffer
	if err := Run(paths, dockerSpecData, &buf); err != nil {
		t.Fatalf("install.Run: %v", err)
	}
	if info, err := os.Stat(sharedDir); err != nil || !info.IsDir() {
		t.Fatalf("shared dir not created: info=%v err=%v", info, err)
	} else if info.Mode().Perm() != 0o755 {
		t.Fatalf("shared dir mode = %v, want 0755", info.Mode().Perm())
	}
	if got, err := os.ReadFile(dropin); err != nil || string(got) != podmanDropinContent(sharedDir) {
		t.Fatalf("podman drop-in wrong: err=%v content=%q", err, got)
	}
	if _, err := os.Stat(dockerSpec); dockerDetected {
		if err != nil {
			t.Fatalf("docker system CDI spec should be written to temp path when docker is detected: %v", err)
		}
	} else if !os.IsNotExist(err) {
		t.Fatalf("docker system CDI spec should not be written when docker is not detected: %v", err)
	}

	specPath := filepath.Join(sharedDir, cdispec.FileName)
	if err := os.WriteFile(specPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	if err := Uninstall(paths, &buf); err != nil {
		t.Fatalf("first uninstall: %v", err)
	}
	for _, p := range []string{specPath, dropin} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s still exists or unexpected stat error: %v", p, err)
		}
	}

	buf.Reset()
	if err := Uninstall(paths, &buf); err != nil {
		t.Fatalf("second uninstall: %v", err)
	}
	if got := buf.String(); !strings.Contains(got, "cdi: already absent") || !strings.Contains(got, "podman: already absent") {
		t.Errorf("second uninstall should report no-ops, got: %s", got)
	}
}

func TestRunBacksUpExistingPodmanDropinThenRepeatedInstallIsIdempotent(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "cdi")
	dropin := filepath.Join(dir, "containers", "containers.conf.d", "nix-direnv-cdi.conf")
	paths := Paths{
		SharedDir:      sharedDir,
		PodmanDropin:   dropin,
		DockerSpecPath: filepath.Join(dir, "etc", "cdi", cdispec.FileName),
	}
	if err := os.MkdirAll(filepath.Dir(dropin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dropin, []byte("# user drop-in\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := Run(paths, dockerSpecData, &buf); err != nil {
		t.Fatalf("install.Run: %v", err)
	}
	if bak, err := os.ReadFile(dropin + ".bak"); err != nil || string(bak) != "# user drop-in\n" {
		t.Fatalf("backup missing or wrong: err=%v content=%q", err, bak)
	}
	bakInfo, err := os.Stat(dropin + ".bak")
	if err != nil {
		t.Fatal(err)
	}
	if bakInfo.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %v, want 0600", bakInfo.Mode().Perm())
	}
	if got, err := os.ReadFile(dropin); err != nil || string(got) != podmanDropinContent(sharedDir) {
		t.Fatalf("podman drop-in wrong: err=%v content=%q", err, got)
	}

	if err := os.WriteFile(dropin+".bak", []byte("sentinel\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	if err := Run(paths, dockerSpecData, &buf); err != nil {
		t.Fatalf("second install.Run: %v", err)
	}
	if !strings.Contains(buf.String(), "podman: already registered") {
		t.Errorf("second run should report podman no-op, got: %s", buf.String())
	}
	if bak, err := os.ReadFile(dropin + ".bak"); err != nil || string(bak) != "sentinel\n" {
		t.Fatalf("idempotent run overwrote backup: err=%v content=%q", err, bak)
	}
}

// TestInstallPodman_WritesThenIdempotentThenBacksUp drives the real file I/O
// against a temp drop-in path: first write creates it, a second identical run
// is a no-op, and a divergent pre-existing file is backed up before rewrite.
func TestInstallPodman_WritesThenIdempotentThenBacksUp(t *testing.T) {
	dropin := filepath.Join(t.TempDir(), "containers", "containers.conf.d", "nix-direnv-cdi.conf")

	var buf bytes.Buffer
	if err := installPodman(dropin, shared, &buf); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if got, err := os.ReadFile(dropin); err != nil || string(got) != podmanDropinContent(shared) {
		t.Fatalf("drop-in content wrong after first write: err=%v", err)
	}

	buf.Reset()
	if err := installPodman(dropin, shared, &buf); err != nil {
		t.Fatalf("second install: %v", err)
	}
	if !strings.Contains(buf.String(), "already registered") {
		t.Errorf("second run should be idempotent, got: %s", buf.String())
	}

	// Simulate a hand-edited drop-in: it must be backed up, then rewritten.
	if err := os.WriteFile(dropin, []byte("# stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	if err := installPodman(dropin, shared, &buf); err != nil {
		t.Fatalf("third install: %v", err)
	}
	bak, err := os.ReadFile(dropin + ".bak")
	if err != nil || string(bak) != "# stale\n" {
		t.Errorf("backup missing or wrong: err=%v content=%q", err, bak)
	}
	if got, _ := os.ReadFile(dropin); string(got) != podmanDropinContent(shared) {
		t.Errorf("drop-in not rewritten to canonical content")
	}
}

func TestInstallDockerSpec_WritesThenIdempotentThenBacksUp(t *testing.T) {
	specPath := filepath.Join(t.TempDir(), "etc", "cdi", cdispec.FileName)

	var buf bytes.Buffer
	if err := installDockerSpec(specPath, dockerSpecData, &buf); err != nil {
		t.Fatalf("first installDockerSpec: %v", err)
	}
	if got, err := os.ReadFile(specPath); err != nil || !bytes.Equal(got, dockerSpecData) {
		t.Fatalf("docker spec content wrong after first write: err=%v content=%q", err, got)
	}
	if info, err := os.Stat(specPath); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("docker spec mode wrong: info=%v err=%v", info, err)
	}

	buf.Reset()
	if err := installDockerSpec(specPath, dockerSpecData, &buf); err != nil {
		t.Fatalf("second installDockerSpec: %v", err)
	}
	if !strings.Contains(buf.String(), "already installed") {
		t.Errorf("second docker run should be idempotent, got: %s", buf.String())
	}
	if _, err := os.Stat(specPath + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("idempotent install should not create backup, stat err=%v", err)
	}

	orig := []byte("stale\n")
	if err := os.WriteFile(specPath, orig, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(specPath, 0o600); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	if err := installDockerSpec(specPath, dockerSpecData, &buf); err != nil {
		t.Fatalf("third installDockerSpec: %v", err)
	}
	if bak, err := os.ReadFile(specPath + ".bak"); err != nil || !bytes.Equal(bak, orig) {
		t.Fatalf("backup missing or wrong: err=%v content=%q", err, bak)
	}
	bakInfo, err := os.Stat(specPath + ".bak")
	if err != nil {
		t.Fatal(err)
	}
	if bakInfo.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %v, want 0600", bakInfo.Mode().Perm())
	}
	if got, err := os.ReadFile(specPath); err != nil || !bytes.Equal(got, dockerSpecData) {
		t.Fatalf("docker spec not rewritten: err=%v content=%q", err, got)
	}
}

func TestRunDockerSystemSpecLifecycleWithTempPath(t *testing.T) {
	fakeDockerOnPath(t)

	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "cdi")
	dropin := filepath.Join(dir, "containers", "containers.conf.d", "nix-direnv-cdi.conf")
	specPath := filepath.Join(dir, "etc", "cdi", cdispec.FileName)
	orig := []byte("stale docker spec\n")
	if err := os.MkdirAll(filepath.Dir(specPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(specPath, orig, 0o640); err != nil {
		t.Fatal(err)
	}
	paths := Paths{
		SharedDir:      sharedDir,
		PodmanDropin:   dropin,
		DockerSpecPath: specPath,
	}

	var buf bytes.Buffer
	if err := Run(paths, dockerSpecData, &buf); err != nil {
		t.Fatalf("install.Run: %v", err)
	}
	if bak, err := os.ReadFile(specPath + ".bak"); err != nil || !bytes.Equal(bak, orig) {
		t.Fatalf("backup missing or wrong: err=%v content=%q", err, bak)
	}
	bakInfo, err := os.Stat(specPath + ".bak")
	if err != nil {
		t.Fatal(err)
	}
	if bakInfo.Mode().Perm() != 0o640 {
		t.Fatalf("backup mode = %v, want 0640", bakInfo.Mode().Perm())
	}
	if got, err := os.ReadFile(specPath); err != nil || !bytes.Equal(got, dockerSpecData) {
		t.Fatalf("docker spec not installed: err=%v content=%q", err, got)
	}

	if err := os.WriteFile(specPath+".bak", []byte("sentinel\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	if err := Run(paths, dockerSpecData, &buf); err != nil {
		t.Fatalf("second install.Run: %v", err)
	}
	if !strings.Contains(buf.String(), "docker: already installed") {
		t.Errorf("second run should report docker no-op, got: %s", buf.String())
	}
	if bak, err := os.ReadFile(specPath + ".bak"); err != nil || string(bak) != "sentinel\n" {
		t.Fatalf("idempotent run overwrote backup: err=%v content=%q", err, bak)
	}

	buf.Reset()
	if err := Uninstall(paths, &buf); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Stat(specPath); !os.IsNotExist(err) {
		t.Fatalf("docker spec still exists or unexpected stat error: %v", err)
	}

	if err := os.WriteFile(specPath+".bak", []byte("post-uninstall sentinel\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	if err := Uninstall(paths, &buf); err != nil {
		t.Fatalf("second uninstall: %v", err)
	}
	if !strings.Contains(buf.String(), "docker: system CDI spec already absent") {
		t.Errorf("second uninstall should report docker no-op, got: %s", buf.String())
	}
	if bak, err := os.ReadFile(specPath + ".bak"); err != nil || string(bak) != "post-uninstall sentinel\n" {
		t.Fatalf("idempotent uninstall overwrote backup: err=%v content=%q", err, bak)
	}
}

func TestRunDockerSpecPermissionFallbackDoesNotFailInstall(t *testing.T) {
	fakeDockerOnPath(t)

	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "cdi")
	parent := filepath.Join(dir, "etc", "cdi")
	specPath := filepath.Join(parent, cdispec.FileName)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(parent, 0o755)
	paths := Paths{
		SharedDir:      sharedDir,
		PodmanDropin:   filepath.Join(dir, "containers", "containers.conf.d", "nix-direnv-cdi.conf"),
		DockerSpecPath: specPath,
	}

	var buf bytes.Buffer
	if err := Run(paths, dockerSpecData, &buf); err != nil {
		t.Fatalf("install.Run: %v", err)
	}
	got := buf.String()
	if os.Geteuid() == 0 {
		t.Skip("permission fallback is not meaningful when tests run as root")
	}
	if !strings.Contains(got, "docker: could not install system CDI spec") ||
		!strings.Contains(got, "install the generated generic CDI spec") ||
		!strings.Contains(got, specPath) ||
		!strings.Contains(got, filepath.Join(sharedDir, cdispec.FileName)) {
		t.Fatalf("permission failure should print Docker manual fallback, got: %s", got)
	}
	if _, err := os.Stat(specPath); !os.IsNotExist(err) {
		t.Fatalf("docker spec should not exist after failed write, stat err=%v", err)
	}
}

func fakeDockerOnPath(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	docker := filepath.Join(dir, "docker")
	if err := os.WriteFile(docker, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func dockerConfigDirExists() bool {
	fi, err := os.Stat("/etc/docker")
	return err == nil && fi.IsDir()
}
