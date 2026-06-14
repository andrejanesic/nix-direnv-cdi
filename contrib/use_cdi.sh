# use_cdi — a direnv helper for nix-direnv-cdi.
#
# Install: copy this function into ~/.config/direnv/direnvrc (or source this
# file from there). Then, after a one-time `nix-direnv-cdi install`, a project's
# .envrc can do:
#
#     use flake
#     use cdi
#
# `use cdi` writes the dev-shell's closure to .direnv/cdi/mounts.json and exports
# $DIRENV_CDI, so you can attach the dev-shell to a container with:
#
#     podman run --device "$DIRENV_CDI" <image> <cmd>
#
# It only needs the materialised .direnv gcroot (created by `use flake`), not
# DIRENV_DIFF, so it is safe to run during .envrc evaluation. Best-effort: if
# `gen` fails (e.g. nix-direnv-cdi not installed yet) the .envrc is not broken.
use_cdi() {
  if ! has nix-direnv-cdi; then
    log_error "use cdi: nix-direnv-cdi not on PATH; skipping"
    return 0
  fi
  eval "$(nix-direnv-cdi gen "$@")" || log_error "use cdi: gen failed; skipping"
}
