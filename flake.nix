{
  description = "pgsavvy - a modern TUI PostgreSQL client";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
        # The locked nixpkgs compiler predates the required security fixes.
        go = pkgs.go_1_26.overrideAttrs (_: {
          version = "1.26.9";
          src = pkgs.fetchurl {
            url = "https://go.dev/dl/go1.26.9.src.tar.gz";
            hash = "sha256-lzXX3Ntls10/pXfwQGRzfAO4nPGitx5uaf4vPG+f1Mo=";
          };
        });
        buildGoModule = pkgs.buildGoModule.override { inherit go; };
      in
      {
        packages.default = buildGoModule {
          pname = "pgsavvy";
          version = self.shortRev or "dev";
          src = self;

          vendorHash = "sha256-CiI7LZjmOg1l6/AwCZexa1j48z7msBnBBKt26F7GZKA=";

          ldflags = [
            "-s"
            "-w"
            "-X main.version=${self.shortRev or "dev"}"
            "-X main.commit=${self.rev or "unknown"}"
            "-X main.date=${if self ? lastModifiedDate then toString self.lastModifiedDate else "unknown"}"
            "-X main.buildSource=nix"
          ];

          preBuild = ''
            test -f RELEASE_NOTES.txt || {
              echo "ERROR: RELEASE_NOTES.txt not found in build sandbox" >&2
              exit 1
            }
          '';

          doCheck = false;
        };

        checks.default = pkgs.runCommand "pgsavvy-smoke" { } ''
          ${self.packages.${system}.default}/bin/pgsavvy --version | tee /dev/stderr | grep -qF '(nix)'
          touch $out
        '';

        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [
            go
            golangci-lint
            go-task
            gopls
            gotools
            git
          ];

          CGO_ENABLED = "0";
          GOTOOLCHAIN = "local";
        };
      }
    );
}
