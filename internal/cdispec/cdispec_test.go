package cdispec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrejanesic/nix-direnv-cdi/internal/devshell"
	specs "tags.cncf.io/container-device-interface/specs-go"
)

func sampleDevShell() *devshell.DevShell {
	return &devshell.DevShell{
		ProjectRoot: "/home/u/proj",
		Prefix: []string{
			"/nix/store/aaa-go/bin",
			"/nix/store/bbb-coreutils/bin",
		},
		Env: map[string]string{
			"IN_NIX_SHELL": "impure",
			"CC":           "gcc",
		},
		Closure: []string{
			"/nix/store/aaa-go",
			"/nix/store/bbb-coreutils",
			"/nix/store/ccc-glibc",
		},
	}
}

func TestBuild_PassesValidate(t *testing.T) {
	ds := sampleDevShell()
	spec, err := Build(ds, "deadbeef", "/usr/local/bin/nix-direnv-cdi")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if err := Validate(spec); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if spec.Version == "" {
		t.Error("spec.Version must not be empty")
	}
}

func TestBuild_KindAndDevice(t *testing.T) {
	spec, err := Build(sampleDevShell(), "deadbeef", "/hook")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Kind != "nix-direnv.cdi/shell" {
		t.Errorf("Kind = %q, want nix-direnv.cdi/shell", spec.Kind)
	}
	if len(spec.Devices) != 1 {
		t.Fatalf("want 1 device, got %d", len(spec.Devices))
	}
	if spec.Devices[0].Name != "deadbeef" {
		t.Errorf("device Name = %q, want deadbeef", spec.Devices[0].Name)
	}
	// All edits must be on the device (so --device kind=name applies them).
	if len(spec.ContainerEdits.Env) != 0 || len(spec.ContainerEdits.Mounts) != 0 || len(spec.ContainerEdits.Hooks) != 0 {
		t.Error("spec-level ContainerEdits must be empty; edits belong on the device")
	}
}

func TestBuild_Mounts(t *testing.T) {
	ds := sampleDevShell()
	spec, err := Build(ds, "x", "/hook")
	if err != nil {
		t.Fatal(err)
	}
	mounts := spec.Devices[0].ContainerEdits.Mounts

	// One ro mount per closure path (source == destination) + one rw workdir.
	byContainer := map[string]*specs.Mount{}
	for _, m := range mounts {
		byContainer[m.ContainerPath] = m
	}
	for _, p := range ds.Closure {
		m, ok := byContainer[p]
		if !ok {
			t.Errorf("missing mount for closure path %q", p)
			continue
		}
		if m.HostPath != p {
			t.Errorf("closure mount HostPath = %q, want %q (source==dest)", m.HostPath, p)
		}
		if !hasAll(m.Options, "ro", "rbind") {
			t.Errorf("closure mount %q options = %v, want ro+rbind", p, m.Options)
		}
	}
	// Project root rw mount.
	rw, ok := byContainer[ds.ProjectRoot]
	if !ok {
		t.Fatalf("missing rw workdir mount for %q", ds.ProjectRoot)
	}
	if rw.HostPath != ds.ProjectRoot || !hasAll(rw.Options, "rw", "rbind") {
		t.Errorf("workdir mount = %+v, want rw+rbind on %q", rw, ds.ProjectRoot)
	}
	if len(mounts) != len(ds.Closure)+1 {
		t.Errorf("got %d mounts, want %d", len(mounts), len(ds.Closure)+1)
	}
}

func TestBuild_Hook(t *testing.T) {
	spec, err := Build(sampleDevShell(), "x", "/usr/local/bin/nix-direnv-cdi")
	if err != nil {
		t.Fatal(err)
	}
	hooks := spec.Devices[0].ContainerEdits.Hooks
	if len(hooks) != 1 {
		t.Fatalf("want exactly 1 hook, got %d", len(hooks))
	}
	h := hooks[0]
	if h.HookName != "createRuntime" {
		t.Errorf("HookName = %q, want createRuntime", h.HookName)
	}
	if h.Path != "/usr/local/bin/nix-direnv-cdi" {
		t.Errorf("Path = %q", h.Path)
	}
	wantArgs := []string{"nix-direnv-cdi", "hook"}
	if len(h.Args) != 2 || h.Args[0] != wantArgs[0] || h.Args[1] != wantArgs[1] {
		t.Errorf("Args = %v, want %v", h.Args, wantArgs)
	}
}

func TestBuild_EnvHasPrefixAndNoPath(t *testing.T) {
	ds := sampleDevShell()
	spec, err := Build(ds, "x", "/hook")
	if err != nil {
		t.Fatal(err)
	}
	env := spec.Devices[0].ContainerEdits.Env

	var hasPrefix bool
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			t.Errorf("Env must never set PATH, found %q", e)
		}
		if e == "DEVSHELL_PREFIX=/nix/store/aaa-go/bin:/nix/store/bbb-coreutils/bin" {
			hasPrefix = true
		}
	}
	if !hasPrefix {
		t.Errorf("DEVSHELL_PREFIX not found or wrong, env = %v", env)
	}
	// Dev-shell vars present.
	if !containsEnv(env, "IN_NIX_SHELL=impure") || !containsEnv(env, "CC=gcc") {
		t.Errorf("dev-shell env vars missing, env = %v", env)
	}
}

func TestWrite_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	spec, err := Build(sampleDevShell(), "deadbeef", "/hook")
	if err != nil {
		t.Fatal(err)
	}
	if err := Write(spec, dir, "deadbeef"); err != nil {
		t.Fatalf("Write: %v", err)
	}

	path := filepath.Join(dir, "nix-direnv-deadbeef.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat written spec: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("file mode = %v, want 0644", info.Mode().Perm())
	}
	// Dir must be traversable (>=0755) for rootless podman.
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm()&0o055 != 0o055 {
		t.Errorf("spec dir mode = %v, must be >=0755 traversable", di.Mode().Perm())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got specs.Spec
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("re-parse written spec: %v", err)
	}
	if got.Kind != spec.Kind || got.Version != spec.Version {
		t.Errorf("round-trip mismatch: kind=%q ver=%q", got.Kind, got.Version)
	}
	if len(got.Devices) != 1 || got.Devices[0].Name != "deadbeef" {
		t.Errorf("round-trip device mismatch: %+v", got.Devices)
	}
	if err := Validate(&got); err != nil {
		t.Errorf("re-parsed spec fails Validate: %v", err)
	}
}

func hasAll(opts []string, want ...string) bool {
	set := map[string]bool{}
	for _, o := range opts {
		set[o] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}

func containsEnv(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}
