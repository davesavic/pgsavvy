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
      in
      {
        packages.default = pkgs.buildGoModule {
          pname = "pgsavvy";
          version = self.shortRev or "dev";
          src = self;

          vendorHash = "sha256-1tdhiCXUtxacOxNhi1zlrQW6YD4vSrdxq/oX/2JBzRk=";

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
            go_1_26
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
