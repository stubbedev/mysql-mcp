package engine

import (
	"fmt"
	"time"

	"github.com/abs/mysql-mcp/internal/config"
	"github.com/go-sql-driver/mysql"
)

// mysqlEngine implements Engine for MySQL and MariaDB (identical wire protocol).
type mysqlEngine struct{}

func (mysqlEngine) Driver() string { return "mysql" }

func (mysqlEngine) DSN(src *config.SourceConfig, netName string) (string, error) {
	if src.DSN != "" {
		return src.DSN, nil
	}
	c := mysql.NewConfig()
	c.User = src.User
	c.Passwd = src.Password
	c.Net = netName
	c.Addr = fmt.Sprintf("%s:%d", src.Host, src.Port)
	c.DBName = src.Database
	c.ParseTime = true
	c.Loc = time.UTC
	c.Params = map[string]string{"charset": "utf8mb4"}
	return c.FormatDSN(), nil
}

// nullableSchema passes the database name through, or nil so the query falls
// back to the connection's current schema via DATABASE().
func nullableSchema(db string) any {
	if db == "" {
		return nil
	}
	return db
}

func (mysqlEngine) ListDatabases() Query {
	return Query{SQL: "SHOW DATABASES"}
}

func (mysqlEngine) ListTables(db string) Query {
	return Query{
		SQL: `SELECT table_name, table_type
		      FROM information_schema.tables
		      WHERE table_schema = COALESCE(?, DATABASE())
		      ORDER BY table_name`,
		Args: []any{nullableSchema(db)},
	}
}

func (mysqlEngine) DescribeTable(db, table string) Query {
	return Query{
		SQL: `SELECT column_name, column_type, is_nullable, column_key,
		             column_default, extra
		      FROM information_schema.columns
		      WHERE table_schema = COALESCE(?, DATABASE()) AND table_name = ?
		      ORDER BY ordinal_position`,
		Args: []any{nullableSchema(db), table},
	}
}
