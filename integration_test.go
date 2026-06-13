package main

// Milestone 4 integration test (PLAN §5 — the nix-gated real-flake test used as
// the main integration test). It is fully end-to-end: it builds the dedicated
// nix dev-shell fixture under testdata/fixture/, materialises a real .direnv via
// `direnv exec`, runs the real `gen`, and proves the dev-shell propagates into a
// stock busybox container through our CDI --device.
//
// Skip conditions (clear t.Skip messages): testing.Short(); or any of
// nix/direnv/podman missing from PATH. podman is the M4 runtime target; docker
// here is a podman shim and its CDI path needs daemon-registered spec dirs, so
// it is detected-and-logged, not exercised.

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// requiredTools are the external binaries the integration test drives.
var requiredTools = []string{"nix", "direnv", "podman"}

const (
	// busyboxImage is the stock base image. `hello` is absent from it, so its
	// presence inside the container is the propagation proof.
	busyboxImage = "busybox"
	// helloOutput is what pkgs.hello prints; the headline assertion.
	helloOutput = "Hello, world!"
	// cmdTimeout bounds each container/gen invocation. The first `gen`
	// materialises .direnv and substitutes hello; container runs mount the
	// whole closure — hence generous.
	cmdTimeout = 5 * time.Minute
)

// TestIntegration_DevShellPropagates is the headline M4 test.
func TestIntegration_DevShellPropagates(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	for _, tool := range requiredTools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("integration test requires %q on PATH (not found): %v", tool, err)
		}
	}
	// docker here resolves to a podman shim; the real docker CDI path needs
	// daemon-registered spec dirs (no per-run --cdi-spec-dir), so we do not
	// mutate daemon.json. Just record the decision.
	if _, err := exec.LookPath("docker"); err == nil {
		t.Log("docker present but docker CDI path deferred (needs daemon-registered spec dirs); podman only for M4")
	}

	// 1. Hermetic fixture copy: never pollute testdata/. The committed fixture
	//    is flake.nix + .envrc + flake.lock (no .direnv).
	fixture := copyFixture(t)

	// 2. Build the binary into a TempDir. The generated spec embeds this path
	//    as the hook (os.Executable() at gen time), so it must persist for the
	//    whole test — a TempDir does (cleaned only at test end). The binary's
	//    dir must be traversable (>=0755) for the rootless-podman hook lookup.
	binDir := t.TempDir()
	chmodTraversable(t, binDir)
	bin := filepath.Join(binDir, "nix-direnv-cdi")
	build(t, bin)

	// 3. direnv allow the fixture so `direnv exec` will load it.
	runChecked(t, fixture, "direnv", "allow", fixture)

	// 4. Generate the spec. `direnv exec <fixture> <bin> gen` runs the real gen
	//    with the fixture's real DIRENV_DIFF; it materialises .direnv (gcroot +
	//    hello closure). The spec dir must be traversable for the CDI resolver.
	specDir := t.TempDir()
	chmodTraversable(t, specDir)
	stdout := genSpec(t, fixture, bin, specDir)

	// stdout line 1 is the device ref (e.g. nix-direnv.cdi/shell=<hash>).
	ref := firstLine(stdout)
	if !strings.HasPrefix(ref, "nix-direnv.cdi/shell=") {
		t.Fatalf("device ref (stdout line 1) = %q, want nix-direnv.cdi/shell=<hash>\nfull stdout:\n%s", ref, stdout)
	}
	t.Logf("device ref: %s", ref)

	// 5. Assertions.

	// T2 (headline): hello is the relative entrypoint; the hook shadow-shims
	// it, the mounted closure makes it runnable. Output must contain the
	// dev-shell-only tool's greeting.
	t.Run("T2_hello_propagates", func(t *testing.T) {
		out := podmanRunDevice(t, specDir, ref, busyboxImage, "hello")
		if !strings.Contains(out, helloOutput) {
			t.Fatalf("T2: dev-shell `hello` did not propagate into the container\nwant substring: %q\ngot:\n%s", helloOutput, out)
		}
		t.Logf("T2 propagation proof:\n%s", strings.TrimSpace(out))
	})

	// T1 (additive PATH): the nix prefix is prepended AND the busybox base
	// (/bin or /usr/bin) is preserved, and hello still prints.
	t.Run("T1_additive_path", func(t *testing.T) {
		out := podmanRunDevice(t, specDir, ref, busyboxImage,
			"sh", "-c", `echo "PATH=$PATH"; hello`)
		pathLine := grepLine(out, "PATH=")
		if !strings.Contains(pathLine, "/nix/store/") {
			t.Errorf("T1: PATH lacks a /nix/store/ prefix dir (not additive)\nPATH line: %s\nfull:\n%s", pathLine, out)
		}
		if !strings.Contains(pathLine, "/bin") {
			t.Errorf("T1: PATH lost the busybox base (/bin)\nPATH line: %s", pathLine)
		}
		if !strings.Contains(out, helloOutput) {
			t.Errorf("T1: hello did not print under the additive PATH\ngot:\n%s", out)
		}
		t.Logf("T1 additive PATH:\n%s", strings.TrimSpace(pathLine))
	})

	// T6 (control): WITHOUT the device, hello must be absent — proves the tool
	// is not in the base image and only arrives via our CDI device.
	t.Run("T6_control_no_device", func(t *testing.T) {
		out := podmanRunNoDevice(t, busyboxImage, "hello")
		if strings.Contains(out, helloOutput) {
			t.Fatalf("T6: hello unexpectedly printed WITHOUT the device — base image is not stock?\ngot:\n%s", out)
		}
		t.Logf("T6 control (hello absent without device), runtime said:\n%s", strings.TrimSpace(out))
	})

	// T3 (optional): a base busybox tool still resolves with the device
	// attached (the dev-shell is additive, not overriding).
	t.Run("T3_base_tool_with_device", func(t *testing.T) {
		out := podmanRunDevice(t, specDir, ref, busyboxImage,
			"sh", "-c", "echo BASE-ECHO-OK")
		if !strings.Contains(out, "BASE-ECHO-OK") {
			t.Fatalf("T3: a base busybox tool failed with the device attached\ngot:\n%s", out)
		}
	})
}

// copyFixture copies the committed fixture (flake.nix, .envrc, flake.lock) into
// a TempDir so testdata/ is never polluted, and makes the dir traversable.
func copyFixture(t *testing.T) string {
	t.Helper()
	src := filepath.Join("testdata", "fixture")
	dst := t.TempDir()
	chmodTraversable(t, dst)
	for _, name := range []string{"flake.nix", ".envrc", "flake.lock"} {
		data, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			t.Fatalf("read fixture %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dst, name), data, 0o644); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
	}
	return dst
}

// build compiles the binary to out. GOFLAGS=-buildvcs=false is already set in
// the environment; the linked-worktree layout would otherwise break VCS stamping.
func build(t *testing.T, out string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", out, ".")
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, combined)
	}
}

// genSpec runs `direnv exec <fixture> <bin> gen --out <specDir>` and returns its
// stdout (line 1 = device ref). direnv exec loads the fixture's .envrc (use
// flake → materialises .direnv, sets DIRENV_DIFF) and runs gen in that env.
func genSpec(t *testing.T, fixture, bin, specDir string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "direnv", "exec", fixture, bin, "gen", "--out", specDir)
	cmd.Dir = fixture
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("gen via direnv exec: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	t.Logf("gen stderr (status):\n%s", strings.TrimSpace(stderr.String()))
	return stdout.String()
}

// podmanRunDevice runs busybox with our CDI device attached and returns the
// combined output. --network=none keeps it hermetic.
func podmanRunDevice(t *testing.T, specDir, ref, image string, args ...string) string {
	t.Helper()
	full := append([]string{
		"run", "--rm", "--network=none",
		"--cdi-spec-dir", specDir, "--device", ref, image,
	}, args...)
	return podman(t, full...)
}

// podmanRunNoDevice runs busybox WITHOUT the device (the control).
func podmanRunNoDevice(t *testing.T, image string, args ...string) string {
	t.Helper()
	full := append([]string{"run", "--rm", "--network=none", image}, args...)
	return podman(t, full...)
}

// podman invokes `podman <args...>` with a per-command timeout and returns the
// combined output. It does NOT fail the test on a non-zero exit (the control
// expects one); callers assert on the output.
func podman(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "podman", args...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("podman %v timed out after %s\noutput:\n%s", args, cmdTimeout, out)
	}
	if err != nil {
		// Non-zero exit can be expected (control); log for diagnosis, let the
		// caller decide based on the output.
		t.Logf("podman %v exited with %v (output below; may be expected)", args, err)
	}
	return string(out)
}

// runChecked runs cmd/args in dir and fails the test on a non-zero exit.
func runChecked(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, combined)
	}
}

// chmodTraversable forces dir AND every ancestor we own (below os.TempDir()) to
// >=0755. PLAN §1 / MVP line 9: rootless podman/crun must traverse the spec dir,
// the hook binary's dir, and the mount sources, all >=0755. Go's t.TempDir()
// nests as $TMPDIR/<TestName><rand>/NNN and creates EVERY level at 0700 —
// including the shared per-test root that parents both the spec dir and the bin
// dir. A single 0700 ancestor yields "unresolvable CDI devices" / a
// hook-not-found, so we widen the whole chain. We stop at os.TempDir() (e.g.
// /tmp): it is already world-traversable (1777) and not ours to chmod; the nix
// store is 0755 already.
func chmodTraversable(t *testing.T, dir string) {
	t.Helper()
	tmpRoot := filepath.Clean(os.TempDir())
	for d := filepath.Clean(dir); d != tmpRoot && d != filepath.Dir(d); d = filepath.Dir(d) {
		if err := os.Chmod(d, 0o755); err != nil {
			t.Fatalf("chmod %s 0755: %v", d, err)
		}
	}
}

// firstLine returns the first non-empty line of s, trimmed.
func firstLine(s string) string {
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			return line
		}
	}
	return ""
}

// grepLine returns the first line of s containing sub (trimmed), or "".
func grepLine(s, sub string) string {
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		if strings.Contains(sc.Text(), sub) {
			return strings.TrimSpace(sc.Text())
		}
	}
	return ""
}
