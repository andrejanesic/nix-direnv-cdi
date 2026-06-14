package cdispec

// Tier A unit tests for the single generic device. No container,
// no nix: pure spec construction + the CNCF validator.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	specs "tags.cncf.io/container-device-interface/specs-go"
)

const hookBin = "/nix/store/abc-nix-direnv-cdi/bin/nix-direnv-cdi"

func TestBuild_PassesValidate(t *testing.T) {
	spec, err := Build(hookBin)
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

func TestBuild_GenericDevice(t *testing.T) {
	spec, err := Build(hookBin)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Kind != Kind {
		t.Errorf("Kind = %q, want %q", spec.Kind, Kind)
	}
	if len(spec.Devices) != 1 {
		t.Fatalf("want 1 device, got %d", len(spec.Devices))
	}
	d := spec.Devices[0]
	if d.Name != Device {
		t.Errorf("device Name = %q, want %q", d.Name, Device)
	}
	// The whole point of the refactor: the device carries NO project data.
	if len(d.ContainerEdits.Env) != 0 {
		t.Errorf("device must carry no env, got %v", d.ContainerEdits.Env)
	}
	if len(d.ContainerEdits.Mounts) != 0 {
		t.Errorf("device must carry no mounts, got %v", d.ContainerEdits.Mounts)
	}
	if len(spec.ContainerEdits.Env)+len(spec.ContainerEdits.Mounts)+len(spec.ContainerEdits.Hooks) != 0 {
		t.Error("spec-level ContainerEdits must be empty")
	}
}

func TestBuild_Hook(t *testing.T) {
	spec, err := Build(hookBin)
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
	if h.Path != hookBin {
		t.Errorf("Path = %q, want %q", h.Path, hookBin)
	}
	if len(h.Args) != 2 || h.Args[0] != "nix-direnv-cdi" || h.Args[1] != "hook" {
		t.Errorf("Args = %v, want [nix-direnv-cdi hook]", h.Args)
	}
}

func TestBuild_RejectsEmptyBinary(t *testing.T) {
	if _, err := Build(""); err == nil {
		t.Error("expected an error for an empty hook binary path")
	}
}

func TestRef_IsConstant(t *testing.T) {
	if Ref != "nix-direnv-cdi.org/env=current" {
		t.Errorf("Ref = %q, want nix-direnv-cdi.org/env=current", Ref)
	}
}

func TestWrite_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	spec, err := Build(hookBin)
	if err != nil {
		t.Fatal(err)
	}
	if err := Write(spec, dir); err != nil {
		t.Fatalf("Write: %v", err)
	}

	path := filepath.Join(dir, FileName)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat written spec: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("file mode = %v, want 0644", info.Mode().Perm())
	}
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
	if len(got.Devices) != 1 || got.Devices[0].Name != Device {
		t.Errorf("round-trip device mismatch: %+v", got.Devices)
	}
	if err := Validate(&got); err != nil {
		t.Errorf("re-parsed spec fails Validate: %v", err)
	}
}
