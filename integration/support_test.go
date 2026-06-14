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

	envContainerCLI         = "NDC_CONTAINER_CLI"
	envDockerCDISpecDir     = "NDC_DOCKER_CDI_SPEC_DIR"
	defaultContainerCLI     = "docker"
	defaultDockerCDISpecDir = "/etc/cdi"
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
	if out, err := exec.Command(p, "info").CombinedOutput(); err != nil {
		t.Fatalf("%s is not usable: %v\n%s", name, err, out)
	}
	if name == "docker" {
		cli.specDir = os.Getenv(envDockerCDISpecDir)
		if cli.specDir == "" {
			cli.specDir = defaultDockerCDISpecDir
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
	return bin
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
	spec := fmt.Sprintf(`{"cdiVersion":"0.6.0","kind":%q,"devices":[`+
		`{"name":%q,"containerEdits":{"hooks":[`+
		`{"hookName":"createRuntime","path":%q,"args":["nix-direnv-cdi","hook"]}]}}]}`+"\n",
		cdispec.Kind, cdispec.Device, binPath)
	if err := os.WriteFile(filepath.Join(dir, "nix-direnv.json"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	chmodTraversable(t, dir)
}

func writeSpecForCLI(t *testing.T, cli containerCLI, binPath string) string {
	t.Helper()
	if cli.name == "docker" {
		writeGenericSpec(t, cli.specDir, binPath)
		return cli.specDir
	}
	specDir := filepath.Join(t.TempDir(), "cdi")
	writeGenericSpec(t, specDir, binPath)
	return specDir
}

func (c containerCLI) deviceArgs(specDir string) []string {
	if c.name == "podman" {
		return []string{"--cdi-spec-dir", specDir, "--device", cdispec.Ref}
	}
	return []string{"--device", cdispec.Ref}
}

func (c containerCLI) direnvPassthroughArgs() []string {
	if c.name != "docker" {
		return nil
	}
	return []string{"--env", "DIRENV_DIR", "--env", "DIRENV_DIFF"}
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
