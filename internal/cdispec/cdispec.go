// Package cdispec builds and validates the CDI spec for a dev-shell using the
// CNCF container-device-interface libraries — never hand-rolled JSON.
package cdispec

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/andrejanesic/nix-direnv-cdi/internal/devshell"
	"tags.cncf.io/container-device-interface/pkg/cdi"
	"tags.cncf.io/container-device-interface/pkg/parser"
	specs "tags.cncf.io/container-device-interface/specs-go"
)

// Spec identity (PLAN §2): kind = "nix-direnv.cdi/shell", one device per project.
const (
	Vendor = "nix-direnv.cdi"
	Class  = "shell"
	Kind   = Vendor + "/" + Class
)

// devshellPrefixEnv is the env var the hook reads to make PATH additive at
// runtime. It carries the colon-joined dev-shell bin dirs.
const devshellPrefixEnv = "DEVSHELL_PREFIX"

// Build constructs the CDI spec for a discovered dev-shell: read-only closure
// mounts + the rw workdir, the dev-shell env minus PATH plus DEVSHELL_PREFIX,
// and a single createRuntime hook pointing at hookBinary. PATH is deliberately
// not set; the hook makes it additive. (PLAN §2, milestone 2.)
func Build(ds *devshell.DevShell, deviceName, hookBinary string) (*specs.Spec, error) {
	if ds == nil {
		return nil, fmt.Errorf("nil dev-shell")
	}
	if deviceName == "" {
		return nil, fmt.Errorf("empty device name")
	}
	if hookBinary == "" {
		return nil, fmt.Errorf("empty hook binary path")
	}

	// Mounts: every closure path read-only (source == destination for nix),
	// plus the project root read-write.
	mounts := make([]*specs.Mount, 0, len(ds.Closure)+1)
	for _, p := range ds.Closure {
		mounts = append(mounts, &specs.Mount{
			HostPath:      p,
			ContainerPath: p,
			Options:       []string{"ro", "rbind"},
		})
	}
	if ds.ProjectRoot != "" {
		mounts = append(mounts, &specs.Mount{
			HostPath:      ds.ProjectRoot,
			ContainerPath: ds.ProjectRoot,
			Options:       []string{"rw", "rbind"},
		})
	}

	// Env: one KEY=VALUE per dev-shell var (already excludes PATH), plus
	// DEVSHELL_PREFIX. Sorted for deterministic output. Never set PATH.
	keys := make([]string, 0, len(ds.Env))
	for k := range ds.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(ds.Env)+1)
	for _, k := range keys {
		env = append(env, k+"="+ds.Env[k])
	}
	env = append(env, devshellPrefixEnv+"="+strings.Join(ds.Prefix, ":"))

	// Hook: exactly one createRuntime hook.
	hook := &specs.Hook{
		HookName: "createRuntime",
		Path:     hookBinary,
		Args:     []string{"nix-direnv-cdi", "hook"},
	}

	spec := &specs.Spec{
		Kind: Kind,
		Devices: []specs.Device{
			{
				Name: deviceName,
				ContainerEdits: specs.ContainerEdits{
					Env:    env,
					Mounts: mounts,
					Hooks:  []*specs.Hook{hook},
				},
			},
		},
	}

	// Set the minimum spec version the assembled edits require.
	version, err := cdi.MinimumRequiredVersion(spec)
	if err != nil {
		return nil, fmt.Errorf("determine spec version: %w", err)
	}
	spec.Version = version

	return spec, nil
}

// Validate runs the CNCF CDI validation over a built spec. It mirrors the
// library's internal Spec.validate(): version, vendor/class/device names, and
// the per-device container edits — all via exported CNCF entry points.
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
	// Spec-level edits (none expected here, but validate if present).
	specEdits := cdi.ContainerEdits{ContainerEdits: &spec.ContainerEdits}
	if err := specEdits.Validate(); err != nil {
		return fmt.Errorf("invalid spec edits: %w", err)
	}
	return nil
}

// Write validates the spec with the CNCF library, then serialises it to dir as
// nix-direnv-<name>.json (0644). The JSON comes from the specs-go struct tags
// (not hand-rolled field names). It ensures dir is traversable (>=0755) so
// rootless podman can resolve it. (PLAN §2, §1 "gotchas", milestone 2.)
func Write(spec *specs.Spec, dir, name string) error {
	if err := Validate(spec); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create spec dir %s: %w", dir, err)
	}
	// PLAN §1 gotcha: a 0700 parent yields "unresolvable CDI devices" under
	// rootless podman. Force the dir traversable.
	if err := os.Chmod(dir, 0o755); err != nil {
		return fmt.Errorf("chmod spec dir %s: %w", dir, err)
	}

	fileName := "nix-direnv-" + name + ".json"
	path := filepath.Join(dir, fileName)

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
