package mcpserver

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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

// resolve with no request (unit-test path) and no client roots falls back to the
// global registry, carrying the server's base timeout.
func TestResolveFallback(t *testing.T) {
	reg := testRegistry(t, `{"sources":{"a":{"engine":"mysql","host":"h"}}}`)
	svc := &Service{reg: reg, queryTimeout: 7 * time.Second}
	r, err := svc.resolve(context.Background(), nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if r.reg != reg || r.queryTimeout != 7*time.Second {
		t.Fatalf("expected fallback to base registry/timeout, got %+v", r)
	}
}

// resolve errors when there is neither a root config nor a global fallback.
func TestResolveNoConfig(t *testing.T) {
	svc := &Service{}
	if _, err := svc.resolve(context.Background(), nil); err == nil {
		t.Fatal("expected error when no config resolves")
	}
}

// rootRegistry builds a per-root registry, serves it from cache on an unchanged
// mtime, and rebuilds it when the config file's mtime advances.
func TestRootRegistryCacheAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), config.RootConfigName)
	if err := os.WriteFile(path, []byte(`{"sources":{"repo":{"engine":"mysql","host":"h"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := &Service{regs: map[string]*cachedReg{}}
	mt := time.Unix(1000, 0)

	r1, err := svc.rootRegistry(path, mt)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if _, err := r1.reg.Get("repo"); err != nil {
		t.Fatalf("expected source 'repo': %v", err)
	}
	if r2, _ := svc.rootRegistry(path, mt); r2 != r1 {
		t.Fatal("same mtime should hit the cache")
	}

	// Edit the config and advance the mtime: expect a rebuild with new sources.
	if err := os.WriteFile(path, []byte(`{"sources":{"other":{"engine":"mysql","host":"h"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	r3, err := svc.rootRegistry(path, mt.Add(time.Second))
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if r3 == r1 {
		t.Fatal("advanced mtime should rebuild")
	}
	if _, err := r3.reg.Get("other"); err != nil {
		t.Fatalf("expected reloaded source 'other': %v", err)
	}
}

// Header-injected roots are request-scoped: two calls carrying different
// X-Mcp-Roots headers resolve to different per-workspace configs, with no bleed
// through shared session state (the multi-tenant / multiplexing-proxy case).
func TestHeaderRootsRequestScoped(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	write := func(dir, src string) {
		p := filepath.Join(dir, config.RootConfigName)
		if err := os.WriteFile(p, []byte(`{"sources":{"`+src+`":{"engine":"mysql","host":"h"}}}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(dirA, "aonly")
	write(dirB, "bonly")

	svc := &Service{regs: map[string]*cachedReg{}, sessions: map[string]*sessionState{}}
	reqFor := func(dir string) *mcp.CallToolRequest {
		return &mcp.CallToolRequest{Extra: &mcp.RequestExtra{
			Header: http.Header{"X-Mcp-Roots": []string{"file://" + dir}},
		}}
	}

	rA, err := svc.resolve(context.Background(), reqFor(dirA))
	if err != nil {
		t.Fatalf("resolve A: %v", err)
	}
	rB, err := svc.resolve(context.Background(), reqFor(dirB))
	if err != nil {
		t.Fatalf("resolve B: %v", err)
	}

	// Each request sees only its own workspace's source, never the other's.
	if _, err := rA.reg.Get("aonly"); err != nil {
		t.Errorf("A should see aonly: %v", err)
	}
	if _, err := rA.reg.Get("bonly"); err == nil {
		t.Error("A must NOT see B's source (cross-contamination)")
	}
	if _, err := rB.reg.Get("bonly"); err != nil {
		t.Errorf("B should see bonly: %v", err)
	}
	if _, err := rB.reg.Get("aonly"); err == nil {
		t.Error("B must NOT see A's source (cross-contamination)")
	}
}

func TestFileURIToPath(t *testing.T) {
	cases := map[string]string{
		"file:///home/u/repo": "/home/u/repo",
		"/home/u/repo":        "/home/u/repo",
		"file:///C:/x":        "C:/x",
		"https://example.com": "",
		"relative/path":       "",
		"":                    "",
	}
	for in, want := range cases {
		if got := fileURIToPath(in); got != want {
			t.Errorf("fileURIToPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRootsFromHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("X-Mcp-Roots", "file:///a, /b")
	h.Add("X-Mcp-Root", "not-a-root") // relative → dropped
	got := rootsFromHeaders(h)
	if len(got) != 2 || got[0] != "/a" || got[1] != "/b" {
		t.Fatalf("got %v, want [/a /b]", got)
	}
}
