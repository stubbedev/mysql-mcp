package engine

import (
	"strings"
	"testing"

	"github.com/stubbedev/mysql-mcp/internal/config"
)

func TestForUnsupported(t *testing.T) {
	if _, err := For("oracle"); err == nil {
		t.Fatal("expected error for unsupported engine")
	}
}

func TestForMariaDBUsesMySQL(t *testing.T) {
	for _, name := range []string{"mysql", "mariadb"} {
		e, err := For(name)
		if err != nil {
			t.Fatalf("For(%q): %v", name, err)
		}
		if e.Driver() != "mysql" {
			t.Errorf("For(%q).Driver() = %q", name, e.Driver())
		}
	}
}

func TestMySQLDSN(t *testing.T) {
	e, _ := For("mysql")
	src := &config.SourceConfig{
		Engine: "mysql", Host: "db.example", Port: 3307,
		User: "ro", Password: "pw", Database: "app",
	}
	dsn, err := e.DSN(src, "tcp")
	if err != nil {
		t.Fatalf("DSN: %v", err)
	}
	for _, want := range []string{"ro:pw@tcp(db.example:3307)/app", "parseTime=true"} {
		if !strings.Contains(dsn, want) {
			t.Errorf("DSN %q missing %q", dsn, want)
		}
	}
}

func TestMySQLDSNUsesNetName(t *testing.T) {
	e, _ := For("mysql")
	src := &config.SourceConfig{Engine: "mysql", Host: "db", Port: 3306}
	dsn, _ := e.DSN(src, "mysqlmcp-ssh-1")
	if !strings.Contains(dsn, "mysqlmcp-ssh-1(db:3306)") {
		t.Errorf("DSN %q does not use the ssh net name", dsn)
	}
}

func TestMySQLDSNPassthrough(t *testing.T) {
	e, _ := For("mysql")
	src := &config.SourceConfig{Engine: "mysql", DSN: "u:p@tcp(h:3306)/d"}
	dsn, _ := e.DSN(src, "tcp")
	if dsn != "u:p@tcp(h:3306)/d" {
		t.Errorf("explicit DSN not passed through: %q", dsn)
	}
}

func TestListTablesArgsNilForEmptyDB(t *testing.T) {
	e, _ := For("mysql")
	q := e.ListTables("")
	if len(q.Args) != 1 || q.Args[0] != nil {
		t.Errorf("expected single nil arg, got %v", q.Args)
	}
	q = e.ListTables("mydb")
	if len(q.Args) != 1 || q.Args[0] != "mydb" {
		t.Errorf("expected arg 'mydb', got %v", q.Args)
	}
}
