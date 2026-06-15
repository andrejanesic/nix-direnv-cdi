package integration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrejanesic/nix-direnv-cdi/internal/cdispec"
)

func TestE2EPodmanInstallRegistrationSmoke(t *testing.T) {
	podman, err := exec.LookPath("podman")
	if err != nil {
		t.Skip("podman not found")
	}
	if dockerHostConfigExists() {
		t.Skip("host /etc/docker exists; CLI install has no flag to redirect Docker system CDI spec safely")
	}
	if dockerSystemSpecExists() {
		t.Skip("host /etc/cdi/nix-direnv.json exists; CLI uninstall has no flag to redirect Docker system CDI spec safely")
	}

	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	if out, err := run(ctx, nil, podman, "info"); err != nil {
		t.Skipf("podman is not usable: %v\n%s", err, out)
	}

	bin := build(t)
	work := t.TempDir()
	xdg := filepath.Join(work, "xdg")
	emptyPath := filepath.Join(work, "empty-path")
	if err := os.MkdirAll(emptyPath, 0o755); err != nil {
		t.Fatal(err)
	}
	installEnv := append(os.Environ(), "XDG_CONFIG_HOME="+xdg, "PATH="+emptyPath)
	runEnv := append(os.Environ(), "XDG_CONFIG_HOME="+xdg)

	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
		defer cancel()
		_, _ = run(ctx, installEnv, bin, "uninstall")
	}()

	out, err := run(ctx, installEnv, bin, "install")
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	out, err = run(ctx, installEnv, bin, "install")
	if err != nil {
		t.Fatalf("second install: %v\n%s", err, out)
	}
	if !strings.Contains(out, "podman: already registered") {
		t.Fatalf("second install should report podman no-op:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(xdg, "cdi", cdispec.FileName)); err != nil {
		t.Fatalf("generic spec not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(xdg, "containers", "containers.conf.d", "nix-direnv-cdi.conf")); err != nil {
		t.Fatalf("podman drop-in not written: %v", err)
	}

	ctx, cancel = context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	out, err = run(ctx, runEnv, podman, "run", "--rm", "--device", cdispec.Ref, busyboxImage, "true")
	if err != nil {
		t.Fatalf("podman did not resolve installed CDI device: %v\n%s", err, out)
	}
}

func dockerHostConfigExists() bool {
	fi, err := os.Stat("/etc/docker")
	return err == nil && fi.IsDir()
}

func dockerSystemSpecExists() bool {
	_, err := os.Stat(filepath.Join("/etc/cdi", cdispec.FileName))
	return err == nil
}
