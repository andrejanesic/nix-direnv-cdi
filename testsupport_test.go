package main

// Shared helpers for the integration tiers (B: synthetic/nix-free, C:
// real-flake). All container tests skip under -short or when the runtime is
// absent, so `go test -short ./...` stays green on a bare runner.

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

const (
	cmdTimeout   = 5 * time.Minute
	busyboxImage = "busybox"
)

// requirePodman skips the test unless podman is available (and not -short).
func requirePodman(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping container integration test in -short mode")
	}
	p, err := exec.LookPath("podman")
	if err != nil {
		t.Skip("podman not found; skipping integration test")
	}
	return p
}

// build compiles the nix-direnv-cdi binary into a fresh, traversable dir and
// returns its path (the path the generated CDI spec will embed as the hook).
func build(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "nix-direnv-cdi")
	out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	chmodTraversable(t, bin)
	return bin
}

// chmodTraversable widens path and every ancestor to >=0755 so the rootless
// createRuntime hook (running as a subuid) can traverse/read it. t.TempDir()
// creates 0700 dirs, which otherwise yield "unresolvable CDI devices" or
// unreadable mounts.json (PLAN §1 gotcha). Best-effort on dirs we don't own.
func chmodTraversable(t *testing.T, path string) {
	t.Helper()
	for p := path; p != "/" && p != "." && p != ""; p = filepath.Dir(p) {
		_ = os.Chmod(p, 0o755)
	}
}

// writeGenericSpec writes the single generic CDI device to dir, with the hook
// path set to binPath. Mirrors cdispec.Build/Write but is independent of the
// binary so Tier B needs no `install` side effects.
func writeGenericSpec(t *testing.T, dir, binPath string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	spec := fmt.Sprintf(`{"cdiVersion":"0.6.0","kind":"nix-direnv.cdi/shell","devices":[`+
		`{"name":"devshell","containerEdits":{"hooks":[`+
		`{"hookName":"createRuntime","path":%q,"args":["nix-direnv-cdi","hook"]}]}}]}`+"\n", binPath)
	if err := os.WriteFile(filepath.Join(dir, "nix-direnv.json"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	chmodTraversable(t, dir)
}

// writeExecScript writes content to path as an executable script (0755),
// creating its parent directory.
func writeExecScript(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

// encodeDirenvDiff builds a DIRENV_DIFF value the way direnv does: padded
// URL-safe base64 of zlib-compressed JSON {"p":prev,"n":next}. The hook's
// decoder (devshell) accepts this.
func encodeDirenvDiff(t *testing.T, prev, next map[string]string) string {
	t.Helper()
	payload, err := json.Marshal(struct {
		P map[string]string `json:"p"`
		N map[string]string `json:"n"`
	}{prev, next})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return base64.URLEncoding.EncodeToString(buf.Bytes())
}

// run executes name with args and extraEnv (appended to os.Environ), returning
// combined output. ctx bounds the runtime.
func run(ctx context.Context, extraEnv []string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
