package main

// Tier C: real-flake end-to-end. Materialises the committed fixture
// dev-shell (provides `hello`), runs the real install/gen flow, and propagates
// the dev-shell into a stock busybox via the generic device. Asserts the real
// nix tool runs inside the container and PATH is additive. Gated on
// nix+direnv+podman (skipped otherwise / under -short).

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrejanesic/nix-direnv-cdi/internal/cdispec"
)

func requireRealFlake(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping real-flake integration test in -short mode")
	}
	for _, b := range []string{"podman", "nix", "direnv"} {
		if _, err := exec.LookPath(b); err != nil {
			t.Skipf("%s not found; skipping real-flake test", b)
		}
	}
	p, _ := exec.LookPath("podman")
	return p
}

// copyFixture materialises testdata/fixture into dst (flake + a fresh .envrc),
// so the real .direnv is built in a temp dir and the committed fixture stays
// clean.
func copyFixture(t *testing.T, dst string) {
	t.Helper()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"flake.nix", "flake.lock"} {
		data, err := os.ReadFile(filepath.Join("testdata", "fixture", f))
		if err != nil {
			t.Fatalf("read fixture %s: %v", f, err)
		}
		if err := os.WriteFile(filepath.Join(dst, f), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dst, ".envrc"), []byte("use flake\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTierC_RealFlakeDevShell(t *testing.T) {
	podman := requireRealFlake(t)
	bin := build(t)

	work := t.TempDir()
	chmodTraversable(t, work)
	fixture := filepath.Join(work, "proj")
	copyFixture(t, fixture)

	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()

	// Allow the .envrc and materialise the real .direnv gcroot (use flake).
	if out, err := run(ctx, nil, "direnv", "allow", fixture); err != nil {
		t.Fatalf("direnv allow: %v\n%s", err, out)
	}
	if out, err := run(ctx, nil, "direnv", "exec", fixture, "true"); err != nil {
		t.Fatalf("materialise .direnv: %v\n%s", err, out)
	}

	// Real `gen` in the fixture context -> writes .direnv/cdi/mounts.json and
	// prints the constant device ref.
	out, err := run(ctx, nil, "direnv", "exec", fixture, bin, "gen")
	if err != nil {
		t.Fatalf("gen: %v\n%s", err, out)
	}
	if !strings.Contains(out, cdispec.Ref) {
		t.Errorf("gen output missing device ref %q:\n%s", cdispec.Ref, out)
	}
	mountsPath := filepath.Join(fixture, ".direnv", "cdi", "mounts.json")
	if _, err := os.Stat(mountsPath); err != nil {
		t.Fatalf("gen did not write mounts.json: %v", err)
	}
	chmodTraversable(t, mountsPath) // widen the .direnv/proj/work chain for the subuid hook

	// The generic device, hook = our built binary.
	specDir := filepath.Join(t.TempDir(), "cdi")
	writeGenericSpec(t, specDir, bin)
	device := []string{"--cdi-spec-dir", specDir, "--device", cdispec.Ref}

	t.Run("hello_propagates_and_path_additive", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
		defer cancel()
		args := []string{"exec", fixture, podman, "run", "--rm"}
		args = append(args, device...)
		args = append(args, busyboxImage, "sh", "-c", "hello; echo \"PATH=$PATH\"")
		out, err := run(ctx, nil, "direnv", args...)
		if err != nil {
			t.Fatalf("podman run: %v\n%s", err, out)
		}
		if !strings.Contains(out, "Hello, world!") { // T2: the real nix tool ran
			t.Errorf("hello did not run inside the container:\n%s", out)
		}
		if !strings.Contains(out, "hello-2.12.3") || !strings.Contains(out, "/bin") { // T1: additive
			t.Errorf("PATH not additive (nix prefix + image base):\n%s", out)
		}
	})

	t.Run("base_tool_still_works_T3", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
		defer cancel()
		args := []string{"exec", fixture, podman, "run", "--rm"}
		args = append(args, device...)
		args = append(args, busyboxImage, "sh", "-c", "ls /bin/busybox >/dev/null && echo BASE_OK")
		out, err := run(ctx, nil, "direnv", args...)
		if err != nil {
			t.Fatalf("podman run: %v\n%s", err, out)
		}
		if !strings.Contains(out, "BASE_OK") {
			t.Errorf("base tool broke with device attached:\n%s", out)
		}
	})

	t.Run("control_no_device_T6", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
		defer cancel()
		out, _ := run(ctx, nil, "direnv", "exec", fixture, podman, "run", "--rm", busyboxImage, "hello")
		if strings.Contains(out, "Hello, world!") {
			t.Errorf("without --device, hello must be absent:\n%s", out)
		}
	})
}
