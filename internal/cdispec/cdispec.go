// Package cdispec builds and validates the CDI spec for a dev-shell using the
// CNCF container-device-interface libraries — never hand-rolled JSON.
package cdispec

import (
	"errors"

	"github.com/andrejanesic/nix-direnv-cdi/internal/devshell"
	specs "tags.cncf.io/container-device-interface/specs-go"
)

// Spec identity (PLAN §2): kind = "nix-direnv.cdi/shell", one device per project.
const (
	Vendor = "nix-direnv.cdi"
	Class  = "shell"
	Kind   = Vendor + "/" + Class
)

// Build constructs the CDI spec for a discovered dev-shell: read-only closure
// mounts + the rw workdir, the dev-shell env minus PATH plus DEVSHELL_PREFIX,
// and a single createRuntime hook pointing at hookBinary. PATH is deliberately
// not set; the hook makes it additive. (PLAN §2, milestone 2.)
func Build(ds *devshell.DevShell, deviceName, hookBinary string) (*specs.Spec, error) {
	return nil, errors.New("cdispec.Build: not implemented (PLAN milestone 2)")
}

// Validate runs the CNCF CDI validator over a built spec.
func Validate(spec *specs.Spec) error {
	return errors.New("cdispec.Validate: not implemented (PLAN milestone 2)")
}

// Write serialises the spec to dir as nix-direnv-<name>.json (0644), ensuring
// dir is traversable (>=0755) so rootless podman can resolve it. (PLAN §2, §1
// "gotchas", milestone 2.)
func Write(spec *specs.Spec, dir, name string) error {
	return errors.New("cdispec.Write: not implemented (PLAN milestone 2)")
}
