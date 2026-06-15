{
  # Example dev-shell for the nix-direnv-cdi walkthrough (see readme.md).
  #
  # The shell pins two things the agent will use inside a throwaway container:
  #   - codex : the coding agent itself, so the agent binary is pinned too
  #   - go    : a stand-in for "your real project toolchain"
  #
  # nix-direnv-cdi mounts THIS shell's /nix/store closure into the container and
  # prepends its bin dirs to PATH, so both tools appear inside a stock image
  # that ships neither. Swap `go` for whatever your project actually needs.
  description = "nix-direnv-cdi example: a pinned agent + toolchain dev-shell";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
    in {
      devShells = forAllSystems (system:
        let pkgs = nixpkgs.legacyPackages.${system};
        in {
          default = pkgs.mkShell {
            packages = with pkgs; [
              codex # OpenAI Codex CLI — the coding agent
              go    # your real, pinned project toolchain
            ];
          };
        });
    };
}
