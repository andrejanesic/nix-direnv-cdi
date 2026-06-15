package integration

// Synthetic integration. Drives the selected container CLI with the generic
// device + a fabricated "project" (a fake prefix tool, a mounts.json pointing
// at it, and a synthetic DIRENV_DIR/DIRENV_DIFF). Proves the dynamic mechanism
// end-to-end without nix: the hook injects the closure via ns-entry and makes
// PATH additive.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrejanesic/nix-direnv-cdi/internal/devshell"
)

func TestSyntheticDynamicMount(t *testing.T) {
	cli := requireContainerCLI(t)
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

	// Synthetic loaded-dev-shell environment for the hook:
	// DIRENV_DIR locates the project; DIRENV_DIFF carries the PATH prefix + a
	// MARKER env var.
	base := "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	diff := encodeDirenvDiff(t,
		map[string]string{"PATH": base},
		map[string]string{"PATH": prefixDir + ":" + base, "MARKER": "synthetic-marker"},
	)
	// The fake "closure" lives under work, not /nix/store, so point the hook's
	// closure-path allowlist there via NIX_STORE_DIR (the same override real
	// relocated stores use).
	devshellEnv := []string{"DIRENV_DIR=-" + work, "DIRENV_DIFF=" + diff, "NIX_STORE_DIR=" + work}

	specDir := writeSpecForCLI(t, cli, bin)
	device := cli.deviceArgs(specDir)

	t.Run("tool_runs_with_additive_path_and_env", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
		defer cancel()
		args := append([]string{"run", "--rm"}, device...)
		args = append(args, cli.direnvPassthroughArgs()...)
		args = append(args, busyboxImage, "prefixtool")
		out, err := run(ctx, devshellEnv, cli.path, args...)
		if err != nil {
			t.Fatalf("%s run: %v\n%s", cli.name, err, out)
		}
		// Core: the dev-shell-only tool ran, so dynamic mount injection worked.
		if !strings.Contains(out, "PREFIXTOOL-RAN") {
			t.Errorf("prefix tool did not run:\n%s", out)
		}
		// PATH is additive: prefix prepended and image base preserved.
		if !strings.Contains(out, "toolPATH="+prefixDir+":") {
			t.Errorf("prefix not prepended to PATH:\n%s", out)
		}
		if !strings.Contains(out, "/bin") {
			t.Errorf("image base PATH not preserved:\n%s", out)
		}
		// Env injection: the dev-shell var reached the process.
		if !strings.Contains(out, "marker=synthetic-marker") {
			t.Errorf("dev-shell env var not injected:\n%s", out)
		}
	})

	t.Run("base_tools_still_work", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
		defer cancel()
		args := append([]string{"run", "--rm"}, device...)
		args = append(args, cli.direnvPassthroughArgs()...)
		args = append(args, busyboxImage, "sh", "-c", "ls /bin/busybox >/dev/null && echo BASE_OK")
		out, err := run(ctx, devshellEnv, cli.path, args...)
		if err != nil {
			t.Fatalf("%s run: %v\n%s", cli.name, err, out)
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
		out, _ := run(ctx, nil, cli.path, args...) // no devshellEnv
		if strings.Contains(out, "PREFIXTOOL-RAN") {
			t.Errorf("gate must be closed without DIRENV_DIR, but the tool ran:\n%s", out)
		}
	})

	t.Run("control_no_device", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
		defer cancel()
		out, _ := run(ctx, devshellEnv, cli.path, "run", "--rm", busyboxImage, "prefixtool")
		if strings.Contains(out, "PREFIXTOOL-RAN") {
			t.Errorf("without --device the tool must not be present:\n%s", out)
		}
	})
}
