{
  description = "mysql-mcp — Model Context Protocol server for MySQL/MariaDB databases";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs =
    { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = import nixpkgs { inherit system; };
        version = if self ? shortRev then self.shortRev else "dev";
      in
      {
        packages.default = pkgs.buildGoModule {
          pname = "mysql-mcp";
          inherit version;
          src = ./.;

          # vendorHash is kept in sync with go.sum by `just sync-flake` (and by
          # CI on master). The `# go-sum:` line caches the go.sum digest so the
          # recipe can skip a nix build when nothing changed.
          # go-sum: b284af1dd0dc8a753706fbd5e27c0e4b5523e2b80dbdd90d0ffa9db7b42d3393
          vendorHash = "sha256-sOvzRBzcHVp7yBLncFiuQJD2li5P61t2ed9AjKGvRYc=";

          subPackages = [ "cmd/mysql-mcp" ];

          ldflags = [
            "-s"
            "-w"
            "-X github.com/stubbedev/mysql-mcp/internal/cli.Version=${version}"
          ];

          meta = with pkgs.lib; {
            description = "MCP server exposing MySQL/MariaDB databases over stdio or HTTP";
            license = licenses.mit;
            mainProgram = "mysql-mcp";
          };
        };

        devShells.default = pkgs.mkShell {
          packages = [
            pkgs.go
            pkgs.gopls
            pkgs.golangci-lint
            pkgs.gomarkdoc
            pkgs.just
          ];
        };

        formatter = pkgs.nixfmt;
      }
    );
}
