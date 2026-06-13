package install

// Tier A unit tests for the pure install logic — the docker daemon.json merge,
// the podman drop-in content, and the I/O orchestration against injected temp
// paths. No real podman/docker, no root: these run under -short.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestPodmanDropinContent(t *testing.T) {
	got := podmanDropinContent(shared)
	for _, want := range []string{"[engine]", "cdi_spec_dirs = [", `"/etc/cdi",`, `"/var/run/cdi",`, `"` + shared + `",`} {
		if !strings.Contains(got, want) {
			t.Errorf("drop-in missing %q\n---\n%s", want, got)
		}
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
