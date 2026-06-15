package integration

// Shared helpers for integration tests. Missing prerequisites are test failures;
// use go test -skip to omit suites intentionally.

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

	"github.com/andrejanesic/nix-direnv-cdi/internal/cdispec"
)

const (
	cmdTimeout   = 5 * time.Minute
	busyboxImage = "busybox"

	envContainerCLI     = "NDC_CONTAINER_CLI"
	envDockerCDISpecDir = "NDC_DOCKER_CDI_SPEC_DIR"
	defaultContainerCLI = "docker"
	// defaultCDISpecDir is a directory both docker and podman scan for CDI specs
	// by default (podman: built-in /etc/cdi). Docker uses it as cli.specDir;
	// podman writes here when usable so no per-run flag/override is needed.
	defaultCDISpecDir = "/etc/cdi"
)

type containerCLI struct {
	name    string
	path    string
	specDir string
}

// requireContainerCLI returns the selected container CLI. By default tests use
// Docker. Set NDC_CONTAINER_CLI=podman to exercise podman instead.
func requireContainerCLI(t *testing.T) containerCLI {
	t.Helper()
	name := os.Getenv(envContainerCLI)
	if name == "" {
		name = defaultContainerCLI
	}

	switch name {
	case "podman", "docker":
	default:
		t.Fatalf("unsupported %s=%q (want podman or docker)", envContainerCLI, name)
	}

	p, err := exec.LookPath(name)
	if err != nil {
		t.Fatalf("%s not found", name)
	}
	cli := containerCLI{name: name, path: p}

	if name == "docker" {
		cli.specDir = os.Getenv(envDockerCDISpecDir)
		if cli.specDir == "" {
			cli.specDir = defaultCDISpecDir
		}

		if err := os.MkdirAll(cli.specDir, 0o755); err != nil {
			t.Fatalf("docker CDI spec dir %s is not writable: %v\n"+
				"configure Docker to read a writable CDI spec directory, or create the default with:\n"+
				"  sudo mkdir -p %s && sudo chown \"$USER:$(id -gn)\" %s",
				cli.specDir, err, cli.specDir, cli.specDir)
		}
		chmodTraversable(t, cli.specDir)
	}

	return cli
}

func requireTools(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		if _, err := exec.LookPath(name); err != nil {
			t.Fatalf("%s not found", name)
		}
	}
}

// build compiles the nix-direnv-cdi binary into a fresh, traversable dir and
// returns its path (the path the generated CDI spec will embed as the hook).
func build(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "nix-direnv-cdi")
	out, err := exec.Command("go", "build", "-buildvcs=false", "-o", bin, "..").CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	chmodTraversable(t, bin)
	// The hook writes an env-independent breadcrumb to <bin>.ndctrace; widen the
	// bin dir so a sub-uid hook can create it (chmodTraversable left it 0755).
	_ = os.Chmod(dir, 0o777)
	return bin
}

// dumpTrace logs the env-independent breadcrumb files (<bin>.<pid>.ndctrace),
// the channel that survives even when NDC_HOOK_LOG does not reach the hook. One
// file per process (gen, hook, mount child), so dump them all.
func dumpTrace(t *testing.T, binPath string) {
	t.Helper()
	matches, _ := filepath.Glob(binPath + ".*.ndctrace")
	if len(matches) == 0 {
		t.Logf("no hook trace files %s.*.ndctrace (hook never ran our code, or could not write one)", binPath)
		return
	}
	for _, p := range matches {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Logf("hook trace %s unreadable: %v", p, err)
			continue
		}
		t.Logf("hook trace %s:\n%s", p, b)
	}
}

// chmodTraversable widens path and every ancestor to >=0755 so the rootless
// createRuntime hook (running as a subuid) can traverse/read it. t.TempDir()
// creates 0700 dirs, which otherwise yield "unresolvable CDI devices" or
// unreadable mounts.json (see docs/internals.md). Best-effort on dirs we don't own.
func chmodTraversable(t *testing.T, path string) {
	t.Helper()
	for p := path; p != "/" && p != "." && p != ""; p = filepath.Dir(p) {
		_ = os.Chmod(p, 0o755)
	}
}

// writeGenericSpec writes the single generic CDI device to dir, with the hook
// path set to binPath. Mirrors cdispec.Build/Write but is independent of the
// binary so integration tests need no `install` side effects.
func writeGenericSpec(t *testing.T, dir, binPath string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Pass the diagnostic trace base via argv (--ndctrace=<binPath>) so the hook
	// can record breadcrumbs without relying on /proc or env reaching it; the
	// test reads them back via dumpTrace globbing <binPath>.*.ndctrace.
	spec := fmt.Sprintf(`{"cdiVersion":"0.6.0","kind":%q,"devices":[`+
		`{"name":%q,"containerEdits":{"hooks":[`+
		`{"hookName":"createRuntime","path":%q,"args":["nix-direnv-cdi","hook",%q]}]}}]}`+"\n",
		cdispec.Kind, cdispec.Device, binPath, "--ndctrace="+binPath)
	specPath := filepath.Join(dir, "nix-direnv.json")
	if err := os.WriteFile(specPath, []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	// Remove the spec on cleanup. When dir is a shared default scan dir (docker's
	// /etc/cdi) the file outlives the test's temp binary it points at; a later
	// test or CI step (e.g. the podman e2e, which also scans /etc/cdi) would then
	// resolve the same device to this STALE spec and exec a deleted binary —
	// "error executing hook (exit code: 1)". t.Cleanup runs at the end of the
	// test, before the process exits, so the next CI step starts clean.
	t.Cleanup(func() { _ = os.Remove(specPath) })
	chmodTraversable(t, dir)
}

// writeSpecForCLI writes the generic CDI device where the selected CLI will find
// it. Docker reads its spec dir from daemon config (cli.specDir).
//
// For podman we prefer a default scan dir (/etc/cdi) that podman reads with no
// extra config — this works on every podman version (the --cdi-spec-dir flag is
// 5.1+) and, crucially, matches the dir podman actually scans on CI, so there is
// no chance of resolving the device to a stale spec in a different dir. The spec
// is removed on cleanup (writeGenericSpec), so it never goes stale. When no
// default dir is writable (local rootful podman without a writable /etc/cdi) we
// fall back to a hermetic temp dir advertised via containers.conf's cdi_spec_dirs
// (field present since containers-common 0.58 / podman 4.9), merged on top of the
// system config with CONTAINERS_CONF_OVERRIDE.
func writeSpecForCLI(t *testing.T, cli containerCLI, binPath string) {
	t.Helper()
	if cli.name == "docker" {
		writeGenericSpec(t, cli.specDir, binPath)
		return
	}
	if dirUsable(defaultCDISpecDir) {
		writeGenericSpec(t, defaultCDISpecDir, binPath)
		return
	}
	base := t.TempDir()
	specDir := filepath.Join(base, "cdi")
	writeGenericSpec(t, specDir, binPath)

	conf := filepath.Join(base, "containers.conf")
	content := fmt.Sprintf("[engine]\ncdi_spec_dirs = [%q]\n", specDir)
	if err := os.WriteFile(conf, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONTAINERS_CONF_OVERRIDE", conf)
}

// dirUsable reports whether dir can be created (if needed) and written to by the
// current user — used to decide whether podman's spec can live in a shared
// default scan dir or must use the temp-dir + containers.conf fallback.
func dirUsable(dir string) bool {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	probe := filepath.Join(dir, ".ndc-probe")
	if err := os.WriteFile(probe, nil, 0o644); err != nil {
		return false
	}
	_ = os.Remove(probe)
	return true
}

// runArgs returns the argument segment that follows the binary path: the `run`
// subcommand, --rm, and the generic device. Both docker and podman locate the
// CDI spec dir out of band (daemon config / containers.conf via
// writeSpecForCLI), so no per-run spec-dir flag is needed.
func (c containerCLI) runArgs() []string {
	return []string{"run", "--rm", "--device", cdispec.Ref}
}

func (c containerCLI) direnvPassthroughArgs() []string {
	if c.name == "docker" {
		return []string{"--env", "DIRENV_DIR", "--env", "DIRENV_DIFF", "--env", "NIX_STORE_DIR"}
	}
	return nil
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

// dumpHookLog logs the contents of the createRuntime hook's debug log (written
// when NDC_HOOK_LOG is set) so a failing run reveals where the hook stopped.
// Under rootless podman the hook runs as a mapped sub-uid and the log is owned
// by that uid (mode 0600), so a direct read fails; fall back to reading it
// inside podman's user namespace via `podman unshare cat`. An unreadable or
// missing log is reported, not fatal.
func dumpHookLog(t *testing.T, cli containerCLI, path string) {
	t.Helper()
	if path == "" {
		return
	}
	if b, err := os.ReadFile(path); err == nil {
		logBlob(t, path, "direct", b)
		return
	}
	if cli.name == "podman" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := run(ctx, nil, cli.path, "unshare", "cat", path)
		if err == nil {
			logBlob(t, path, "podman unshare", []byte(out))
			return
		}
		// Surface whether the file exists at all (and its owner/mode).
		lsCtx, lsCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer lsCancel()
		ls, _ := run(lsCtx, nil, cli.path, "unshare", "ls", "-la", filepath.Dir(path))
		t.Logf("hook log %s unreadable via unshare: %v\ndir listing:\n%s", path, err, ls)
		return
	}
	t.Logf("hook log %s unreadable", path)
}

func logBlob(t *testing.T, path, via string, b []byte) {
	t.Helper()
	if len(b) == 0 {
		t.Logf("hook log %s (%s) is empty (hook produced no diagnostics)", path, via)
		return
	}
	t.Logf("hook log %s (%s):\n%s", path, via, b)
}

// run executes name with args and extraEnv (appended to os.Environ), returning
// combined output. ctx bounds the runtime.
func run(ctx context.Context, extraEnv []string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
