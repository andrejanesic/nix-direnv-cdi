package main

// Tier B: synthetic, nix-free integration. Drives real podman with
// the generic device + a fabricated "project" (a fake prefix tool, a
// mounts.json pointing at it, and a synthetic DIRENV_DIR/DIRENV_DIFF). Proves
// the dynamic mechanism end-to-end without nix: the hook injects the closure
// via ns-entry and makes PATH additive. Needs only podman.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrejanesic/nix-direnv-cdi/internal/devshell"
)

func TestTierB_SyntheticDynamicMount(t *testing.T) {
	podman := requirePodman(t)
	bin := build(t)

	// Fabricate a fake dev-shell: one self-contained tool (a pure shell script,
	// so there is no real closure to mount — its own dir IS the "closure").
	work := t.TempDir()
	chmodTraversable(t, work)
	prefixDir := filepath.Join(work, "prefix")
	writeExecScript(t, filepath.Join(prefixDir, "prefixtool"),
		"#!/bin/sh\necho PREFIXTOOL-RAN\necho \"toolPATH=$PATH\"\necho \"marker=$MARKER\"\n")
	chmodTraversable(t, prefixDir)

	// Per-project mounts.json: the "closure" is the prefix dir (source==dest).
	mountsPath := filepath.Join(work, ".direnv", "cdi", "mounts.json")
	if err := devshell.WriteMounts(mountsPath, []string{prefixDir}); err != nil {
		t.Fatal(err)
	}
	chmodTraversable(t, filepath.Join(work, ".direnv"))

	// The generic device, hook path = our built binary.
	specDir := filepath.Join(t.TempDir(), "cdi")
	writeGenericSpec(t, specDir, bin)

	// Synthetic loaded-dev-shell environment for podman (the hook inherits it):
	// DIRENV_DIR locates the project; DIRENV_DIFF carries the PATH prefix + a
	// MARKER env var.
	base := "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	diff := encodeDirenvDiff(t,
		map[string]string{"PATH": base},
		map[string]string{"PATH": prefixDir + ":" + base, "MARKER": "tierb-marker"},
	)
	devshellEnv := []string{"DIRENV_DIR=-" + work, "DIRENV_DIFF=" + diff}

	device := []string{"--cdi-spec-dir", specDir, "--device", "nix-direnv.cdi/shell=devshell"}

	t.Run("tool_runs_with_additive_path_and_env", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
		defer cancel()
		args := append([]string{"run", "--rm"}, device...)
		args = append(args, busyboxImage, "prefixtool")
		out, err := run(ctx, devshellEnv, podman, args...)
		if err != nil {
			t.Fatalf("podman run: %v\n%s", err, out)
		}
		// Core: the dev-shell-only tool ran (T2/T7 -> dynamic mount worked).
		if !strings.Contains(out, "PREFIXTOOL-RAN") {
			t.Errorf("prefix tool did not run:\n%s", out)
		}
		// T1: PATH is additive (prefix prepended AND image base preserved).
		if !strings.Contains(out, "toolPATH="+prefixDir+":") {
			t.Errorf("prefix not prepended to PATH:\n%s", out)
		}
		if !strings.Contains(out, "/bin") {
			t.Errorf("image base PATH not preserved:\n%s", out)
		}
		// Env injection: the dev-shell var reached the process.
		if !strings.Contains(out, "marker=tierb-marker") {
			t.Errorf("dev-shell env var not injected:\n%s", out)
		}
	})

	t.Run("base_tools_still_work_T3", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
		defer cancel()
		args := append([]string{"run", "--rm"}, device...)
		args = append(args, busyboxImage, "sh", "-c", "ls /bin/busybox >/dev/null && echo BASE_OK")
		out, err := run(ctx, devshellEnv, podman, args...)
		if err != nil {
			t.Fatalf("podman run: %v\n%s", err, out)
		}
		if !strings.Contains(out, "BASE_OK") {
			t.Errorf("base tools broke with device attached:\n%s", out)
		}
	})

	t.Run("gate_closed_without_direnv", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
		defer cancel()
		// Device attached but NO DIRENV_DIR -> hook is inert -> tool not found.
		args := append([]string{"run", "--rm"}, device...)
		args = append(args, busyboxImage, "prefixtool")
		out, _ := run(ctx, nil, podman, args...) // no devshellEnv
		if strings.Contains(out, "PREFIXTOOL-RAN") {
			t.Errorf("gate must be closed without DIRENV_DIR, but the tool ran:\n%s", out)
		}
	})

	t.Run("control_no_device", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
		defer cancel()
		out, _ := run(ctx, devshellEnv, podman, "run", "--rm", busyboxImage, "prefixtool")
		if strings.Contains(out, "PREFIXTOOL-RAN") {
			t.Errorf("without --device the tool must not be present:\n%s", out)
		}
	})
}
