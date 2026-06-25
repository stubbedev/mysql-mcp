// Package engine abstracts the SQL dialect specifics behind a small interface
// so additional databases (PostgreSQL/CockroachDB, SQLite) can be added without
// touching the source registry or MCP layer. This release implements MySQL,
// which also covers MariaDB.
package engine

import (
	"fmt"

	"github.com/abs/mysql-mcp/internal/config"
)

// Query is a SQL string plus its positional arguments.
type Query struct {
	SQL  string
	Args []any
}

// Engine encapsulates one database dialect: how to build its DSN and how to
// introspect its catalog.
type Engine interface {
	// Driver is the database/sql driver name to pass to sql.Open.
	Driver() string

	// DSN builds a data source name for the source. netName is the network
	// identifier registered with the driver: "tcp" for direct connections, or a
	// per-source SSH dialer name when the source is tunneled.
	DSN(src *config.SourceConfig, netName string) (string, error)

	// ListDatabases returns the query listing all databases/schemas.
	ListDatabases() Query
	// ListTables returns the query listing tables in db (current schema if empty).
	ListTables(db string) Query
	// DescribeTable returns the query describing a table's columns.
	DescribeTable(db, table string) Query
}

// For returns the Engine implementing the named dialect.
func For(name string) (Engine, error) {
	switch name {
	case "mysql", "mariadb":
		return mysqlEngine{}, nil
	default:
		return nil, fmt.Errorf("unsupported engine %q", name)
	}
}
