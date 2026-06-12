{
  pkgs ? import <nixpkgs> { },
}:

with pkgs;
mkShell {
  packages = [
    # Go toolchain — build & test the nix-direnv-cdi binary
    go
    gopls # language server
    gotools # goimports, etc.
    go-tools # staticcheck
    delve # dlv debugger

    # The mechanism under test: needed for the Tier C real-flake smoke
    # tests and for dogfooding the dev-shell -> container passthrough.
    nix-direnv
  ];
}
