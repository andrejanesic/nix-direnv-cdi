package integration

// End-to-end integration. Materialises the committed fixture dev-shell
// (provides `hello`), runs the real gen flow, and propagates the dev-shell into
// a stock busybox via the selected container CLI. Asserts the real nix tool runs
// inside the container and PATH is additive.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrejanesic/nix-direnv-cdi/internal/cdispec"
)

func requireE2E(t *testing.T) containerCLI {
	t.Helper()
	cli := requireContainerCLI(t)
	requireTools(t, "nix", "direnv", "git")
	return cli
}

// copyFixture materialises fixture into dst so the real .direnv is built in a
// temp dir and the committed fixture stays clean.
func copyFixture(t *testing.T, dst string) {
	t.Helper()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{".envrc", "flake.nix", "flake.lock"} {
		data, err := os.ReadFile(filepath.Join("fixture", f))
		if err != nil {
			t.Fatalf("read fixture %s: %v", f, err)
		}
		if err := os.WriteFile(filepath.Join(dst, f), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	if out, err := run(ctx, nil, "git", "-C", dst, "init", "-q"); err != nil {
		t.Fatalf("git init fixture: %v\n%s", err, out)
	}
	if out, err := run(ctx, nil, "git", "-C", dst, "add", "."); err != nil {
		t.Fatalf("git add fixture: %v\n%s", err, out)
	}
}

func TestE2EFlakeDevShell(t *testing.T) {
	cli := requireE2E(t)
	bin := build(t)

	work := t.TempDir()
	chmodTraversable(t, work)
	fixture := filepath.Join(work, "proj")
	copyFixture(t, fixture)
	direnvEnv := []string{"XDG_DATA_HOME=" + filepath.Join(work, "xdg-data")}

	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()

	// Allow the .envrc and materialise the real .direnv gcroot (use flake).
	if out, err := run(ctx, direnvEnv, "direnv", "allow", fixture); err != nil {
		t.Fatalf("direnv allow: %v\n%s", err, out)
	}
	if out, err := run(ctx, direnvEnv, "direnv", "exec", fixture, "true"); err != nil {
		t.Fatalf("materialise .direnv: %v\n%s", err, out)
	}

	// Real `gen` in the fixture context -> writes .direnv/cdi/mounts.json and
	// prints the constant device ref.
	out, err := run(ctx, direnvEnv, "direnv", "exec", fixture, bin, "gen")
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
	writeSpecForCLI(t, cli, bin)

	// crun swallows the createRuntime hook's stderr, so point the hook at a log
	// file (best-effort debug knob) and dump it when a run fails — this is the
	// only window into why the hook misbehaved on a given runtime/host. Under
	// rootless podman the hook runs as a mapped sub-uid, so the log dir must be
	// world-writable for the hook to create the file (and dumpHookLog reads it
	// back through `podman unshare`).
	hookLogDir := filepath.Join(work, "hooklog")
	if err := os.MkdirAll(hookLogDir, 0o777); err != nil {
		t.Fatal(err)
	}
	chmodTraversable(t, hookLogDir) // make ancestors traversable (0755)...
	_ = os.Chmod(hookLogDir, 0o777) // ...then force the leaf world-writable
	hookLog := filepath.Join(hookLogDir, "hook.log")
	t.Setenv("NDC_HOOK_LOG", hookLog)

	t.Run("hello_propagates_and_path_additive", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
		defer cancel()
		args := []string{"exec", fixture, "env", "-u", "XDG_DATA_HOME", cli.path}
		args = append(args, cli.runArgs()...)
		args = append(args, cli.direnvPassthroughArgs()...)
		args = append(args, "--env", "NDC_HOOK_LOG") // diagnostic: container-env channel for the hook log
		args = append(args, busyboxImage, "sh", "-c", "hello; echo \"PATH=$PATH\"")
		out, err := run(ctx, direnvEnv, "direnv", args...)
		if err != nil {
			dumpHookLog(t, cli, hookLog)
			t.Fatalf("%s run: %v\n%s", cli.name, err, out)
		}
		if !strings.Contains(out, "Hello, world!") {
			t.Errorf("hello did not run inside the container:\n%s", out)
		}
		if !strings.Contains(out, "hello-2.12.3") || !strings.Contains(out, "/bin") {
			t.Errorf("PATH not additive (nix prefix + image base):\n%s", out)
		}
	})

	t.Run("base_tool_still_works", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
		defer cancel()
		args := []string{"exec", fixture, "env", "-u", "XDG_DATA_HOME", cli.path}
		args = append(args, cli.runArgs()...)
		args = append(args, cli.direnvPassthroughArgs()...)
		args = append(args, "--env", "NDC_HOOK_LOG") // diagnostic: container-env channel for the hook log
		args = append(args, busyboxImage, "sh", "-c", "ls /bin/busybox >/dev/null && echo BASE_OK")
		out, err := run(ctx, direnvEnv, "direnv", args...)
		if err != nil {
			dumpHookLog(t, cli, hookLog)
			t.Fatalf("%s run: %v\n%s", cli.name, err, out)
		}
		if !strings.Contains(out, "BASE_OK") {
			t.Errorf("base tool broke with device attached:\n%s", out)
		}
	})

	t.Run("gate_closed_without_direnv", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
		defer cancel()
		args := []string{"-u", "DIRENV_DIR", "-u", "DIRENV_DIFF", cli.path}
		args = append(args, cli.runArgs()...)
		args = append(args, busyboxImage, "hello")
		out, _ := run(ctx, nil, "env", args...)
		if strings.Contains(out, "Hello, world!") {
			t.Errorf("device must be inert without DIRENV_DIR, but hello ran:\n%s", out)
		}
	})

	t.Run("control_no_device", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
		defer cancel()
		out, _ := run(ctx, direnvEnv, "direnv", "exec", fixture, "env", "-u", "XDG_DATA_HOME", cli.path, "run", "--rm", busyboxImage, "hello")
		if strings.Contains(out, "Hello, world!") {
			t.Errorf("without --device, hello must be absent:\n%s", out)
		}
	})
}
