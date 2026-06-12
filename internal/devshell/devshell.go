// Package devshell discovers a nix-direnv dev-shell from the loaded direnv
// environment and the .direnv gcroot: the additive PATH prefix, the exported
// environment (minus PATH), and the full nix store closure to mount.
package devshell

import "errors"

// DevShell is a discovered nix-direnv dev-shell.
type DevShell struct {
	// ProjectRoot is the project/workdir, from ${DIRENV_DIR#-} (fallback $PWD).
	ProjectRoot string
	// Prefix is the set of nix-store bin dirs that form the additive PATH
	// prefix (colon-joined into DEVSHELL_PREFIX).
	Prefix []string
	// Env holds the exported dev-shell variables, excluding PATH.
	Env map[string]string
	// Closure is every store path from `nix-store -qR` over the gcroot; each is
	// mounted read-only into the container.
	Closure []string
}

// Discover inspects os.Environ (the loaded direnv environment) and walks the
// closure from .direnv/flake-profile-* via `nix-store -qR`. (PLAN §3, milestone 2.)
func Discover() (*DevShell, error) {
	return nil, errors.New("devshell.Discover: not implemented (PLAN milestone 2)")
}
