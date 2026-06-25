# mysql-mcp

A [Model Context Protocol](https://modelcontextprotocol.io) server that exposes
MySQL/MariaDB databases — local or reached over SSH — to MCP clients such as
Claude. It speaks both **stdio** and **streamable HTTP**, is designed to run on
the same machine as its consumer, and works behind an MCP proxy.

Built on the official [`modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk).

## Features

- **Multiple named sources** in one config — point at several databases at once.
- **Local or SSH-tunneled** sources, with enforced host-key verification.
- **Read-only sources**: a real SQL parser rejects anything that isn't a pure
  read; the `write_query` tool is refused entirely.
- **stdio and HTTP transports**; the HTTP transport supports stateless mode,
  JSON responses, and configurable DNS-rebind protection for use behind proxies.
- **XDG-compliant JSON config** with `${ENV_VAR}` expansion for secrets.
- **Auto-generated** JSON Schema, config docs, API docs and Nix `flake.nix`
  vendorHash, all maintained by CI.

## Install

With Nix (builds the pinned binary):

```sh
nix run github:stubbedev/mysql-mcp -- version
nix profile install github:stubbedev/mysql-mcp
```

With Go:

```sh
go install github.com/stubbedev/mysql-mcp/cmd/mysql-mcp@latest
```

From source:

```sh
go build -o mysql-mcp ./cmd/mysql-mcp
```

## Configuration

`mysql-mcp` reads a JSON config file. By default it looks for
`$XDG_CONFIG_HOME/mysql-mcp/config.json` (falling back to the XDG search path);
override with `--config`.

- Field reference: [docs/configuration.md](docs/configuration.md)
- JSON Schema (for editor completion/validation): [schema/config.schema.json](schema/config.schema.json)
- Worked example: [config.example.json](config.example.json)

```jsonc
{
  "$schema": "https://github.com/stubbedev/mysql-mcp/raw/master/schema/config.schema.json",
  "sources": {
    "local": {
      "engine": "mysql",
      "host": "127.0.0.1",
      "user": "root",
      "password": "${LOCAL_DB_PASSWORD}",
      "database": "app",
      "readonly": true
    }
  }
}
```

Secret-bearing fields (`password`, `dsn`, SSH credentials) support `${ENV_VAR}`
expansion, so secrets need not live in the file.

### SSH (remote) sources

Add an `ssh` block to a source to tunnel the database connection. The database
`host`/`port` are resolved from the SSH server's perspective. Host keys are
**always** verified against `known_hosts` (defaults to `~/.ssh/known_hosts`).

```jsonc
"reporting": {
  "engine": "mariadb",
  "host": "10.0.0.5", "port": 3306,
  "user": "readonly", "database": "analytics", "readonly": true,
  "ssh": {
    "host": "bastion.example.com", "user": "deploy",
    "private_key_path": "~/.ssh/id_ed25519"
  }
}
```

### Read-only & security

- A source with `"readonly": true` rejects `write_query` and any non
  SELECT/SHOW/DESCRIBE/EXPLAIN statement (validated with a MySQL parser, not a
  prefix match). `read_query` always enforces this regardless of the source.
- `--read-only` on the command line forces **every** source read-only.
- The HTTP transport defaults to loopback (`127.0.0.1`) with DNS-rebind
  protection on. Only relax these when a trusted proxy sits in front.

## Running

### stdio

The default transport. This is what most MCP clients launch directly, and what
stdio MCP proxies wrap:

```sh
mysql-mcp serve --config ~/.config/mysql-mcp/config.json
```

Example client (Claude Desktop / Claude Code) entry:

```json
{
  "mcpServers": {
    "mysql": { "command": "mysql-mcp", "args": ["serve"] }
  }
}
```

### HTTP

```sh
mysql-mcp serve --transport http --http-addr 127.0.0.1:7000
```

The MCP endpoint is served at `http.path` (default `/mcp`); `GET /healthz`
returns `ok`.

### Behind an MCP proxy

- **stdio proxies** (which spawn the binary and pipe stdin/stdout) work out of
  the box — all logging goes to stderr, never stdout.
- **HTTP proxies/load balancers**: set `http.stateless: true` so requests are
  not pinned to a session, and `http.json_response: true` if the proxy does not
  forward Server-Sent Events. If the proxy forwards a non-localhost `Host`
  header from a trusted network, set `http.disable_dns_rebind_protection: true`.

## Tools

| Tool | Purpose |
|------|---------|
| `list_sources` | List configured sources (engine, remote, readonly). |
| `list_databases` | List databases/schemas on a source. |
| `list_tables` | List tables in a database. |
| `describe_table` | Describe a table's columns. |
| `read_query` | Run a read-only query and return rows. |
| `write_query` | Run a write/DDL statement (refused on read-only sources). |
| `explain_query` | Return the query plan via EXPLAIN. |

Every tool takes a `source` argument naming the source from `list_sources`.

## Development

Common tasks are driven by [`just`](https://github.com/casey/just) — run `just`
to list them.

```sh
nix develop          # go, gopls, golangci-lint, gomarkdoc, just
just build           # ./bin/mysql-mcp
just lint            # gofmt + go vet + golangci-lint
just test            # go test ./...
just test-race       # race detector + coverage
just test-e2e        # spin a throwaway docker MySQL and exercise the tools
just install-hooks   # enable the pre-commit gofmt/vet gate
just check           # full merge gate: lint + test + sync-*
```

Derived artifacts are regenerated by the `sync-*` recipes (CI also runs these
and commits the result on master):

```sh
just sync-schema     # schema/config.schema.json
just sync-docs       # docs/configuration.md + docs/api.md
just sync-flake      # flake.nix vendorHash (cached against go.sum)
```

## Supported engines

This release ships **MySQL** and **MariaDB** (identical wire protocol). The
internal `engine` interface is structured so PostgreSQL/CockroachDB (via pgx)
and SQLite can be added without changing the source registry or MCP layer.

## License

MIT — see [LICENSE](LICENSE).
