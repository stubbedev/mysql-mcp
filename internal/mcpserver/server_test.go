package mcpserver

import (
	"context"
	"testing"

	"github.com/stubbedev/mysql-mcp/internal/config"
	"github.com/stubbedev/mysql-mcp/internal/source"
)

func testRegistry(t *testing.T, json string) *source.Registry {
	t.Helper()
	cfg, err := config.Parse([]byte(json))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	reg, err := source.NewRegistry(cfg)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return reg
}

func TestListSources(t *testing.T) {
	reg := testRegistry(t, `{"sources":{"a":{"engine":"mysql","host":"h","readonly":true},"b":{"engine":"mariadb","host":"h"}}}`)
	svc := &Service{reg: reg}
	_, out, err := svc.listSources(context.Background(), nil, struct{}{})
	if err != nil {
		t.Fatalf("listSources: %v", err)
	}
	if len(out.Sources) != 2 {
		t.Fatalf("got %d sources", len(out.Sources))
	}
	// Sorted by name: a then b.
	if out.Sources[0].Name != "a" || !out.Sources[0].Readonly {
		t.Errorf("source a: %+v", out.Sources[0])
	}
	if out.Sources[1].Name != "b" || out.Sources[1].Readonly {
		t.Errorf("source b: %+v", out.Sources[1])
	}
}

func TestReadonlyOverride(t *testing.T) {
	reg := testRegistry(t, `{"sources":{"b":{"engine":"mysql","host":"h"}}}`)
	svc := &Service{reg: reg, readonlyOverride: true}
	src, _ := reg.Get("b")
	if !svc.isReadonly(src) {
		t.Error("override should force read-only")
	}
}

func TestWriteQueryRejectedOnReadonly(t *testing.T) {
	reg := testRegistry(t, `{"sources":{"a":{"engine":"mysql","host":"h","readonly":true}}}`)
	svc := &Service{reg: reg}
	res, _, err := svc.writeQuery(context.Background(), nil, queryInput{Source: "a", SQL: "DELETE FROM t"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatal("expected an error result for write on read-only source")
	}
}

func TestUnknownSource(t *testing.T) {
	reg := testRegistry(t, `{"sources":{"a":{"engine":"mysql","host":"h"}}}`)
	svc := &Service{reg: reg}
	res, _, _ := svc.listDatabases(context.Background(), nil, sourceInput{Source: "nope"})
	if res == nil || !res.IsError {
		t.Fatal("expected error result for unknown source")
	}
}
