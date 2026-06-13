package main

// Tier A unit tests for resolvePlacement — the pure shared-vs-local placement
// decision (PLAN §2). No container, nix, direnv, or filesystem I/O: the shared
// dir resolver is injected, so these run under -short.

import (
	"errors"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/andrejanesic/nix-direnv-cdi/internal/cdispec"
	"github.com/andrejanesic/nix-direnv-cdi/internal/fingerprint"
)

// hexFingerprint matches a fingerprint device name: exactly Length lowercase
// hex chars.
var hexFingerprint = regexp.MustCompile(`^[0-9a-f]{16}$`)

func TestResolvePlacement(t *testing.T) {
	const (
		projectRoot   = "/home/u/proj"
		sharedDirPath = "/home/u/.config/cdi"
		outOverride   = "/tmp/custom-out"
	)
	// Injected resolver: deterministic, never errors. Marks if it was called so
	// we can assert --out short-circuits it.
	called := false
	sharedDir := func() (string, error) {
		called = true
		return sharedDirPath, nil
	}

	wantFingerprint := fingerprint.Compute(projectRoot)
	if !hexFingerprint.MatchString(wantFingerprint) {
		t.Fatalf("precondition: fingerprint %q is not 16 hex chars", wantFingerprint)
	}

	t.Run("shared_default", func(t *testing.T) {
		called = false
		dir, name, err := resolvePlacement("shared", "", projectRoot, sharedDir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if dir != sharedDirPath {
			t.Errorf("dir = %q, want %q", dir, sharedDirPath)
		}
		if name != wantFingerprint {
			t.Errorf("deviceName = %q, want fingerprint %q", name, wantFingerprint)
		}
		if !hexFingerprint.MatchString(name) {
			t.Errorf("deviceName %q is not 16 hex chars", name)
		}
		if !called {
			t.Error("shared default should consult the shared dir resolver")
		}
	})

	t.Run("shared_out_override", func(t *testing.T) {
		called = false
		dir, name, err := resolvePlacement("shared", outOverride, projectRoot, sharedDir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if dir != outOverride {
			t.Errorf("dir = %q, want override %q", dir, outOverride)
		}
		if name != wantFingerprint {
			t.Errorf("deviceName = %q, want fingerprint %q", name, wantFingerprint)
		}
		if called {
			t.Error("--out should short-circuit the shared dir resolver")
		}
	})

	t.Run("local_default", func(t *testing.T) {
		called = false
		dir, name, err := resolvePlacement("local", "", projectRoot, sharedDir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		wantDir := filepath.Join(projectRoot, ".direnv", "cdi")
		if dir != wantDir {
			t.Errorf("dir = %q, want %q", dir, wantDir)
		}
		if name != cdispec.Class {
			t.Errorf("deviceName = %q, want constant %q", name, cdispec.Class)
		}
		if name != "shell" {
			t.Errorf("deviceName = %q, want %q", name, "shell")
		}
		if called {
			t.Error("local mode must not consult the shared dir resolver")
		}
	})

	t.Run("local_out_override", func(t *testing.T) {
		called = false
		dir, name, err := resolvePlacement("local", outOverride, projectRoot, sharedDir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if dir != outOverride {
			t.Errorf("dir = %q, want override %q", dir, outOverride)
		}
		if name != "shell" {
			t.Errorf("deviceName = %q, want %q", name, "shell")
		}
		if called {
			t.Error("local --out must not consult the shared dir resolver")
		}
	})

	t.Run("unknown_mode_errors", func(t *testing.T) {
		called = false
		_, _, err := resolvePlacement("bogus", "", projectRoot, sharedDir)
		if err == nil {
			t.Fatal("expected an error for an unknown mode, got nil")
		}
		if called {
			t.Error("unknown mode must not consult the shared dir resolver")
		}
	})

	t.Run("shared_resolver_error_propagates", func(t *testing.T) {
		boom := errors.New("no home dir")
		_, _, err := resolvePlacement("shared", "", projectRoot, func() (string, error) {
			return "", boom
		})
		if !errors.Is(err, boom) {
			t.Errorf("err = %v, want it to wrap %v", err, boom)
		}
	})
}
