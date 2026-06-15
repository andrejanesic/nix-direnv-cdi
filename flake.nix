{
  description = "CDI for secure Nix direnv passthrough from host to container";

  # Tracks nixos-unstable deliberately: the build needs a recent Go toolchain
  # (see `go` in go.mod) that the stable channels lag behind. The exact revision
  # is pinned in flake.lock for reproducibility; run `nix flake update`
  # intentionally (and re-review) rather than relying on the floating branch.
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
      sourceRef = self.ref or "";
      isReleaseRef = builtins.match "v[0-9]+\\.[0-9]+\\.[0-9]+.*" sourceRef != null;
      # Version metadata stamped into the binary. Pinned tag refs report the
      # SemVer tag when Nix exposes it; otherwise the build remains traceable by
      # revision.
      version =
        if isReleaseRef
        then sourceRef
        else self.shortRev or self.dirtyShortRev or "dev";
      commit = self.shortRev or self.dirtyShortRev or "unknown";
      buildDate = self.lastModifiedDate or "unknown";
    in {
      packages = forAllSystems (system:
        let pkgs = nixpkgs.legacyPackages.${system};
        in {
          default = self.packages.${system}.nix-direnv-cdi;

          # The single static binary. The generated CDI spec embeds this
          # binary's own path (os.Executable -> /proc/self/exe) as the
          # createRuntime hook command; once installed, that resolves through
          # the profile symlink to the immutable, 0755-traversable
          # /nix/store/.../bin/nix-direnv-cdi.
          nix-direnv-cdi = pkgs.buildGoModule {
            pname = "nix-direnv-cdi";
            inherit version;
            src = self;
            # Recompute with `nix build` after dependency changes: set to
            # nixpkgs.lib.fakeHash, build, copy the "got:" hash from the error.
            vendorHash = "sha256-6zq/Gv1EsuMegTZwX8vOY9xWDpvgI3k/KD8WNLuOGCs=";
            env.CGO_ENABLED = 0;
            ldflags = [
              "-s"
              "-w"
              "-X main.version=${version}"
              "-X main.commit=${commit}"
              "-X main.buildDate=${buildDate}"
            ];
            # Tests are tiered (Tier B/C need podman/nix/direnv) and gated at
            # runtime; the build sandbox has none, so don't run them here.
            doCheck = false;
            meta = {
              description = "Expose a nix-direnv dev-shell inside any OCI container via a CDI device";
              mainProgram = "nix-direnv-cdi";
              platforms = systems;
              license = pkgs.lib.licenses.asl20;
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
