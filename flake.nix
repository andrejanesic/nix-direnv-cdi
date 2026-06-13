{
  description = "CDI for secure Nix direnv passthrough from host to container";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
      # Version: the flake's own git rev when clean, dirty rev when not, else
      # "dev". Stamped into the binary via -ldflags so `nix-direnv-cdi version`
      # reports something traceable.
      version = self.shortRev or self.dirtyShortRev or "dev";
    in {
      packages = forAllSystems (system:
        let pkgs = nixpkgs.legacyPackages.${system};
        in {
          default = self.packages.${system}.nix-direnv-cdi;

          # The single static binary. The generated CDI spec embeds this
          # binary's own path (os.Executable -> /proc/self/exe) as the
          # createRuntime hook command; once installed, that resolves through
          # the profile symlink to the immutable, 0755-traversable
          # /nix/store/.../bin/nix-direnv-cdi (PLAN milestone 8).
          nix-direnv-cdi = pkgs.buildGoModule {
            pname = "nix-direnv-cdi";
            inherit version;
            src = self;
            # Recompute with `nix build` after dependency changes: set to
            # nixpkgs.lib.fakeHash, build, copy the "got:" hash from the error.
            vendorHash = "sha256-6zq/Gv1EsuMegTZwX8vOY9xWDpvgI3k/KD8WNLuOGCs=";
            env.CGO_ENABLED = 0;
            ldflags = [ "-s" "-w" "-X main.version=${version}" ];
            # Tests are tiered (Tier B/C need podman/nix/direnv) and gated at
            # runtime; the build sandbox has none, so don't run them here.
            doCheck = false;
            meta = {
              description = "Expose a nix-direnv dev-shell inside any OCI container via a CDI device";
              mainProgram = "nix-direnv-cdi";
              platforms = systems;
            };
          };
        });

      apps = forAllSystems (system: {
        default = {
          type = "app";
          program = "${self.packages.${system}.nix-direnv-cdi}/bin/nix-direnv-cdi";
        };
      });

      devShells = forAllSystems (system: {
        default = import ./shell.nix { pkgs = nixpkgs.legacyPackages.${system}; };
      });
    };
}
