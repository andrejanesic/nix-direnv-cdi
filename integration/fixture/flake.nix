{
  # Milestone 4 integration-test fixture. A minimal dev-shell whose only
  # package is `pkgs.hello` — a tiny tool that prints "Hello, world!" and is
  # absent from busybox. Its presence inside a stock container is the
  # dev-shell-propagation proof.
  #
  # nixpkgs is pinned (flake.lock) to the SAME rev the parent project uses, so
  # its source is already fetched/cached and `hello` substitutes instantly.
  description = "nix-direnv-cdi integration-test fixture (hello dev-shell)";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
    in {
      devShells = forAllSystems (system:
        let pkgs = nixpkgs.legacyPackages.${system};
        in {
          default = pkgs.mkShell { packages = [ pkgs.hello ]; };
        });
    };
}
