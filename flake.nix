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

          # vendorHash is maintained automatically by CI (.github/workflows/generate.yml).
          # Run `scripts/update-vendor-hash.sh` to refresh it locally.
          vendorHash = "sha256-sOvzRBzcHVp7yBLncFiuQJD2li5P61t2ed9AjKGvRYc=";

          subPackages = [ "cmd/mysql-mcp" ];

          ldflags = [
            "-s"
            "-w"
            "-X github.com/abs/mysql-mcp/internal/cli.Version=${version}"
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
          ];
        };

        formatter = pkgs.nixfmt;
      }
    );
}
