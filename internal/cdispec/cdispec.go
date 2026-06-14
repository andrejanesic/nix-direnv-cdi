// Package cdispec builds and validates the single, generic CDI device for
// nix-direnv-cdi using the CNCF container-device-interface libraries — never
// hand-rolled JSON. The device carries no project data: its only containerEdit
// is a createRuntime hook that injects the dev-shell dynamically at run time.
// One device serves every project.
package cdispec

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"tags.cncf.io/container-device-interface/pkg/cdi"
	"tags.cncf.io/container-device-interface/pkg/parser"
	specs "tags.cncf.io/container-device-interface/specs-go"
)

// Spec identity. There is exactly one device, named Device, for all
// projects; Ref is the constant reference users pass to --device.
const (
	Vendor = "nix-direnv.cdi"
	Class  = "shell"
	Kind   = Vendor + "/" + Class
	Device = "devshell"
	Ref    = Kind + "=" + Device

	// FileName is the spec file written into the registered CDI dir.
	FileName = "nix-direnv.json"
)

// Build constructs the single generic device: one createRuntime hook pointing
// at hookBinary, and nothing else — no env, no mounts. The hook injects the
// project's closure + dev-shell env at run time.
func Build(hookBinary string) (*specs.Spec, error) {
	if hookBinary == "" {
		return nil, fmt.Errorf("empty hook binary path")
	}

	hook := &specs.Hook{
		HookName: "createRuntime",
		Path:     hookBinary,
		Args:     []string{"nix-direnv-cdi", "hook"},
	}

	spec := &specs.Spec{
		Kind: Kind,
		Devices: []specs.Device{
			{
				Name: Device,
				ContainerEdits: specs.ContainerEdits{
					Hooks: []*specs.Hook{hook},
				},
			},
		},
	}

	version, err := cdi.MinimumRequiredVersion(spec)
	if err != nil {
		return nil, fmt.Errorf("determine spec version: %w", err)
	}
	spec.Version = version

	return spec, nil
}

// Validate runs the CNCF CDI validation over a built spec via exported entry
// points: version, vendor/class/device names, and the per-device edits.
func Validate(spec *specs.Spec) error {
	if spec == nil {
		return fmt.Errorf("nil spec")
	}
	if err := specs.ValidateVersion(spec); err != nil {
		return fmt.Errorf("invalid version: %w", err)
	}
	vendor, class := parser.ParseQualifier(spec.Kind)
	if err := parser.ValidateVendorName(vendor); err != nil {
		return fmt.Errorf("invalid vendor: %w", err)
	}
	if err := parser.ValidateClassName(class); err != nil {
		return fmt.Errorf("invalid class: %w", err)
	}
	if len(spec.Devices) == 0 {
		return fmt.Errorf("invalid spec, no devices")
	}
	seen := map[string]bool{}
	for i := range spec.Devices {
		d := &spec.Devices[i]
		if err := parser.ValidateDeviceName(d.Name); err != nil {
			return fmt.Errorf("invalid device name %q: %w", d.Name, err)
		}
		if seen[d.Name] {
			return fmt.Errorf("invalid spec, multiple device %q", d.Name)
		}
		seen[d.Name] = true
		edits := cdi.ContainerEdits{ContainerEdits: &d.ContainerEdits}
		if err := edits.Validate(); err != nil {
			return fmt.Errorf("invalid device %q: %w", d.Name, err)
		}
	}
	specEdits := cdi.ContainerEdits{ContainerEdits: &spec.ContainerEdits}
	if err := specEdits.Validate(); err != nil {
		return fmt.Errorf("invalid spec edits: %w", err)
	}
	return nil
}

// Write validates the spec, then serialises it to dir/FileName (0644). It
// ensures dir is traversable (>=0755) so rootless podman can resolve it
// (see docs/internals.md). The JSON comes from the specs-go struct tags.
func Write(spec *specs.Spec, dir string) error {
	if err := Validate(spec); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create spec dir %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		return fmt.Errorf("chmod spec dir %s: %w", dir, err)
	}

	path := filepath.Join(dir, FileName)
	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal spec: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write spec %s: %w", path, err)
	}
	return nil
}
