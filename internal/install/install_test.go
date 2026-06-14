package install

// Unit tests for the pure install logic — the docker daemon.json merge,
// the podman drop-in content, and the I/O orchestration against injected temp
// paths. No real podman/docker, no root: these run under -short.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrejanesic/nix-direnv-cdi/internal/cdispec"
)

const shared = "/home/u/.config/cdi"

func TestMergeDockerSpecDirs_SeedsDefaultsWhenAbsent(t *testing.T) {
	out, changed, err := mergeDockerSpecDirs(nil, shared)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true (key was absent)")
	}
	dirs := decodeDirs(t, out)
	want := []string{"/etc/cdi", "/var/run/cdi", shared}
	if !equal(dirs, want) {
		t.Errorf("cdi-spec-dirs = %v, want %v", dirs, want)
	}
}

func TestMergeDockerSpecDirs_PreservesOtherKeys(t *testing.T) {
	in := []byte(`{"data-root":"/var/lib/docker","cdi-spec-dirs":["/etc/cdi"],"log-level":"warn"}`)
	out, changed, err := mergeDockerSpecDirs(in, shared)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	var cfg map[string]any
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if cfg["data-root"] != "/var/lib/docker" || cfg["log-level"] != "warn" {
		t.Errorf("other keys not preserved: %v", cfg)
	}
	dirs := decodeDirs(t, out)
	if !equal(dirs, []string{"/etc/cdi", shared}) {
		t.Errorf("cdi-spec-dirs = %v, want existing list with shared appended", dirs)
	}
}

func TestMergeDockerSpecDirs_IdempotentWhenPresent(t *testing.T) {
	in := []byte(`{"cdi-spec-dirs":["/etc/cdi","` + shared + `"]}`)
	out, changed, err := mergeDockerSpecDirs(in, shared)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if changed {
		t.Error("changed = true, want false (shared already present)")
	}
	if out != nil {
		t.Errorf("out = %q, want nil when no change", out)
	}
}

func TestMergeDockerSpecDirs_RejectsMalformed(t *testing.T) {
	if _, _, err := mergeDockerSpecDirs([]byte(`{`), shared); err == nil {
		t.Error("expected an error for malformed JSON")
	}
	if _, _, err := mergeDockerSpecDirs([]byte(`{"cdi-spec-dirs":"nope"}`), shared); err == nil {
		t.Error("expected an error when cdi-spec-dirs is not an array")
	}
}

func TestRemoveDockerSpecDir_RemovesOnlySharedDir(t *testing.T) {
	in := []byte(`{"cdi-spec-dirs":["/etc/cdi","` + shared + `","/opt/cdi"],"log-level":"warn"}`)
	out, changed, err := removeDockerSpecDir(in, shared)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	dirs := decodeDirs(t, out)
	if !equal(dirs, []string{"/etc/cdi", "/opt/cdi"}) {
		t.Errorf("cdi-spec-dirs = %v, want shared removed and others preserved", dirs)
	}
	var cfg map[string]any
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if cfg["log-level"] != "warn" {
		t.Errorf("unrelated key not preserved: %v", cfg)
	}
}

func TestRemoveDockerSpecDir_IdempotentWhenAbsent(t *testing.T) {
	for _, in := range [][]byte{
		[]byte(`{"cdi-spec-dirs":["/etc/cdi","/opt/cdi"]}`),
		[]byte(`{"log-level":"warn"}`),
		nil,
	} {
		out, changed, err := removeDockerSpecDir(in, shared)
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", in, err)
		}
		if changed {
			t.Fatalf("changed = true for %q, want false", in)
		}
		if out != nil {
			t.Fatalf("out = %q, want nil for no-op", out)
		}
	}
}

func TestRemoveDockerSpecDir_RemovesKeyWhenOnlySharedDir(t *testing.T) {
	in := []byte(`{"cdi-spec-dirs":["` + shared + `"],"log-level":"warn"}`)
	out, changed, err := removeDockerSpecDir(in, shared)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	var cfg map[string]any
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if _, ok := cfg["cdi-spec-dirs"]; ok {
		t.Errorf("cdi-spec-dirs still present: %v", cfg)
	}
	if cfg["log-level"] != "warn" {
		t.Errorf("unrelated key not preserved: %v", cfg)
	}
}

func TestRemoveDockerSpecDir_RejectsMalformed(t *testing.T) {
	if _, _, err := removeDockerSpecDir([]byte(`{`), shared); err == nil {
		t.Error("expected an error for malformed JSON")
	}
	if _, _, err := removeDockerSpecDir([]byte(`{"cdi-spec-dirs":"nope"}`), shared); err == nil {
		t.Error("expected an error when cdi-spec-dirs is not an array")
	}
	if _, _, err := removeDockerSpecDir([]byte(`{"cdi-spec-dirs":[42]}`), shared); err == nil {
		t.Error("expected an error when cdi-spec-dirs contains a non-string")
	}
}

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
	daemon := filepath.Join(dir, "docker", "daemon.json")
	spec := filepath.Join(sharedDir, cdispec.FileName)
	for _, p := range []string{sharedDir, filepath.Dir(dropin)} {
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

	paths := Paths{
		SharedDir:        sharedDir,
		PodmanDropin:     dropin,
		DockerDaemonJSON: daemon,
	}
	var buf bytes.Buffer
	if err := Uninstall(paths, &buf); err != nil {
		t.Fatalf("first uninstall: %v", err)
	}
	for _, p := range []string{spec, dropin} {
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

// TestInstallDocker_WritesAndBacksUp drives the docker path against a temp
// daemon.json: an existing file is backed up and the merged result is valid.
func TestInstallDocker_WritesAndBacksUp(t *testing.T) {
	daemon := filepath.Join(t.TempDir(), "daemon.json")
	orig := `{"log-level":"warn"}`
	if err := os.WriteFile(daemon, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := installDocker(daemon, shared, &buf); err != nil {
		t.Fatalf("installDocker: %v", err)
	}
	if bak, err := os.ReadFile(daemon + ".bak"); err != nil || string(bak) != orig {
		t.Errorf("backup missing or wrong: err=%v content=%q", err, bak)
	}
	dirs := decodeDirsFromFile(t, daemon)
	if !contains(dirs, shared) {
		t.Errorf("daemon.json cdi-spec-dirs %v missing %q", dirs, shared)
	}

	// Second run is idempotent (no second backup overwrite needed, no change).
	buf.Reset()
	if err := installDocker(daemon, shared, &buf); err != nil {
		t.Fatalf("second installDocker: %v", err)
	}
	if !strings.Contains(buf.String(), "already registered") {
		t.Errorf("second docker run should be idempotent, got: %s", buf.String())
	}
}

func TestUninstallDocker_RemovesSharedPreservesSettingsAndBacksUp(t *testing.T) {
	daemon := filepath.Join(t.TempDir(), "daemon.json")
	orig := `{"cdi-spec-dirs":["/etc/cdi","` + shared + `","/opt/cdi"],"data-root":"/var/lib/docker"}`
	if err := os.WriteFile(daemon, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := uninstallDocker(daemon, shared, &buf); err != nil {
		t.Fatalf("uninstallDocker: %v", err)
	}
	bakInfo, err := os.Stat(daemon + ".bak")
	if err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if bakInfo.Mode().Perm() != 0o600 {
		t.Errorf("backup mode = %v, want 0600", bakInfo.Mode().Perm())
	}
	if bak, err := os.ReadFile(daemon + ".bak"); err != nil || string(bak) != orig {
		t.Errorf("backup missing or wrong: err=%v content=%q", err, bak)
	}

	dirs := decodeDirsFromFile(t, daemon)
	if !equal(dirs, []string{"/etc/cdi", "/opt/cdi"}) {
		t.Errorf("cdi-spec-dirs = %v, want shared removed", dirs)
	}
	var cfg map[string]any
	data, err := os.ReadFile(daemon)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["data-root"] != "/var/lib/docker" {
		t.Errorf("unrelated docker setting not preserved: %v", cfg)
	}
}

func TestUninstallDocker_NoOpDoesNotBackUp(t *testing.T) {
	daemon := filepath.Join(t.TempDir(), "daemon.json")
	orig := `{"cdi-spec-dirs":["/etc/cdi"],"log-level":"warn"}`
	if err := os.WriteFile(daemon, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := uninstallDocker(daemon, shared, &buf); err != nil {
		t.Fatalf("uninstallDocker: %v", err)
	}
	if !strings.Contains(buf.String(), "already unregistered") {
		t.Errorf("no-op should be reported, got: %s", buf.String())
	}
	if _, err := os.Stat(daemon + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("backup should not be created for no-op, stat err=%v", err)
	}
	if got, err := os.ReadFile(daemon); err != nil || string(got) != orig {
		t.Fatalf("daemon changed on no-op: err=%v content=%q", err, got)
	}
}

func TestUninstallDocker_AbsentIsNoOp(t *testing.T) {
	daemon := filepath.Join(t.TempDir(), "daemon.json")
	var buf bytes.Buffer
	if err := uninstallDocker(daemon, shared, &buf); err != nil {
		t.Fatalf("uninstallDocker absent: %v", err)
	}
	if !strings.Contains(buf.String(), "daemon.json absent") {
		t.Errorf("absent daemon should be reported, got: %s", buf.String())
	}
	if _, err := os.Stat(daemon + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("backup should not be created for absent daemon, stat err=%v", err)
	}
}

func decodeDirs(t *testing.T, jsonBytes []byte) []string {
	t.Helper()
	var cfg struct {
		Dirs []string `json:"cdi-spec-dirs"`
	}
	if err := json.Unmarshal(jsonBytes, &cfg); err != nil {
		t.Fatalf("decode cdi-spec-dirs: %v", err)
	}
	return cfg.Dirs
}

func decodeDirsFromFile(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return decodeDirs(t, data)
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func contains(xs []string, x string) bool {
	for _, e := range xs {
		if e == x {
			return true
		}
	}
	return false
}
