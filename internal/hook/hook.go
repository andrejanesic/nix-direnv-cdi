// Package hook implements the createRuntime OCI hook that wraps the container
// entrypoint so the dev-shell prefix is prepended to PATH additively. The hook
// runs in the host namespace after the mount namespace and mounts exist, the
// one stage that can both read config.json and write the final rootfs. See
// PLAN.md §1 for the entrypoint-resolution algorithm and its limitations.
package hook

import (
	"errors"
	"io"

	"github.com/andrejanesic/nix-direnv-cdi/internal/ociconfig"
)

// Run executes the createRuntime hook:
//
//  1. read the OCI container State from stdin (yields the bundle path),
//  2. load <bundle>/config.json,
//  3. resolve process.args[0] across prefix:imagePATH, map container->host via
//     the mounts, and install a wrapper/shim that prepends the dev-shell prefix
//     to PATH before exec'ing the real entrypoint.
//
// It is best-effort: the caller ignores the returned error and exits 0 so the
// container is never broken.
func Run(in io.Reader) error {
	state, err := ociconfig.ReadState(in)
	if err != nil {
		return err
	}
	spec, err := ociconfig.Load(state.Bundle)
	if err != nil {
		return err
	}
	_ = spec
	return errors.New("hook.Run: entrypoint wrapping not implemented (PLAN milestone 3)")
}
