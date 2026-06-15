# Non-flakes alternative to flake.nix, for projects that use `use nix` instead
# of `use flake`. It builds the SAME dev-shell (pinned `codex` + `go`) from a
# pinned nixpkgs revision, so nix-direnv-cdi behaves identically either way.
#
# nixpkgs is pinned by commit; `fetchTarball` without a sha256 is impure but
# keeps the example copy-pasteable. For a fully reproducible pin, add the
# `sha256` nix prints on first evaluation, or just use the flake (flake.lock
# pins it for you).
{ pkgs ? import (fetchTarball {
    url = "https://github.com/NixOS/nixpkgs/archive/d233902339c02a9c334e7e593de68855ad26c4cb.tar.gz";
  }) { }
}:

pkgs.mkShell {
  packages = with pkgs; [
    codex # OpenAI Codex CLI — the coding agent
    go    # your real, pinned project toolchain
  ];
}
