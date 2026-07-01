// Package mcpserver wires the source registry into an MCP server, exposing a
// small set of tools for inspecting and querying the configured databases.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stubbedev/mysql-mcp/internal/config"
	"github.com/stubbedev/mysql-mcp/internal/source"
	"github.com/stubbedev/mysql-mcp/internal/sqlguard"
)

// defaultRowLimit caps read_query rows when the caller omits an explicit limit.
const defaultRowLimit = 1000

// Service holds the dependencies shared by all tool handlers. It resolves the
// registry to use per call: a client that exposes a workspace root containing a
// RootConfigName file gets that config; everything else falls back to the
// server's global registry (reg), which may be nil in roots-only mode.
type Service struct {
	reg              *source.Registry // global fallback; nil when started with no config
	readonlyOverride bool
	queryTimeout     time.Duration

	srv *mcp.Server

	mu       sync.Mutex
	sessions map[string]*sessionState // SDK session id → roots cache
	regs     map[string]*cachedReg    // per-root config path → built registry
}

// resolved is the registry plus per-config query timeout a single tool call runs
// against, after root resolution.
type resolved struct {
	reg          *source.Registry
	queryTimeout time.Duration
}

// cachedReg is a per-root registry cached by config file path, keyed on the
// file's mtime so an edited config reloads on the next call.
type cachedReg struct {
	mtime time.Time
	res   *resolved
}

// New builds an MCP server exposing the database tools. base is the global
// fallback registry (nil for roots-only mode). readonlyOverride forces every
// source to behave as read-only regardless of its config (the global
// --read-only flag). baseTimeout caps each query/statement on the fallback
// registry (<=0 disables it); per-root configs carry their own timeout.
func New(base *source.Registry, baseTimeout time.Duration, version string, readonlyOverride bool) *mcp.Server {
	svc := &Service{
		reg:              base,
		readonlyOverride: readonlyOverride,
		queryTimeout:     baseTimeout,
		sessions:         map[string]*sessionState{},
		regs:             map[string]*cachedReg{},
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "mysql-mcp", Version: version}, &mcp.ServerOptions{
		RootsListChangedHandler: svc.onRootsChanged,
	})
	svc.srv = srv
	svc.register(srv)
	go svc.sweepSessions()
	return srv
}

// withTimeout derives a per-query context bounded by d. When d<=0 it returns
// ctx with a no-op cancel.
func withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}

// resolve picks the registry for a tool call. Header-injected roots are checked
// first and are request-scoped — never cached on shared session state, so a
// proxy multiplexing several clients over one session may vary them per request
// without cross-contamination. Next comes the client's roots/list (cached per
// session), then the global fallback. A nil request (unit tests) or a client
// without roots resolves straight to the fallback.
func (s *Service) resolve(ctx context.Context, req *mcp.CallToolRequest) (*resolved, error) {
	if path, mtime, ok := firstRootConfig(headerRootsOf(req)); ok {
		return s.rootRegistry(path, mtime)
	}
	if path, mtime, ok := firstRootConfig(s.sessionRoots(ctx, req)); ok {
		return s.rootRegistry(path, mtime)
	}
	if s.reg != nil {
		return &resolved{reg: s.reg, queryTimeout: s.queryTimeout}, nil
	}
	return nil, fmt.Errorf("no config resolved: add %s to your workspace root, or start the server with --config", config.RootConfigName)
}

// firstRootConfig returns the config path (and its mtime) of the first root that
// holds a RootConfigName file.
func firstRootConfig(roots []string) (string, time.Time, bool) {
	for _, root := range roots {
		path := filepath.Join(root, config.RootConfigName)
		if fi, err := os.Stat(path); err == nil && !fi.IsDir() {
			return path, fi.ModTime(), true
		}
	}
	return "", time.Time{}, false
}

// headerRootsOf extracts request-scoped roots injected via HTTP headers.
func headerRootsOf(req *mcp.CallToolRequest) []string {
	if req == nil || req.Extra == nil {
		return nil
	}
	return rootsFromHeaders(req.Extra.Header)
}

// sessionRoots returns the calling client's workspace roots from roots/list,
// cached per session. Clients that did not advertise the roots capability
// (including every stateless request, which re-initializes with default state)
// get no cache entry at all — this is what keeps the session map from growing
// under stateless HTTP load.
func (s *Service) sessionRoots(ctx context.Context, req *mcp.CallToolRequest) []string {
	if req == nil || req.Session == nil || !rootsCapable(req.Session) {
		return nil
	}
	return s.sessionFor(req).rootPaths(ctx)
}

// sessionFor returns the roots/list cache for the calling client, creating it on
// first use. Only reached for roots-capable sessions (see sessionRoots).
func (s *Service) sessionFor(req *mcp.CallToolRequest) *sessionState {
	ss := req.Session
	id := ss.ID()

	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.sessions[id]
	if st == nil {
		st = &sessionState{ss: ss}
		s.sessions[id] = st
	}
	return st
}

// rootsCapable reports whether the client advertised the roots capability at
// initialize, so we skip a doomed roots/list round-trip — and a leaked session
// entry — when it did not.
func rootsCapable(ss *mcp.ServerSession) bool {
	ip := ss.InitializeParams()
	return ip != nil && ip.Capabilities != nil && ip.Capabilities.RootsV2 != nil
}

// rootRegistry returns the registry built from the config at path, cached by
// (path, mtime). An edited config (new mtime) rebuilds and retires the old
// pools.
//
// A config edit (new mtime) rebuilds and closes the old registry, which drains
// its pools and deregisters its SSH dialers — so reloads do not leak.
//
// ponytail: distinct workspaces still each retain a registry for the life of the
// process (bounded by how many configs one server sees — a handful locally). Add
// refcount/LRU eviction only if a long-lived multi-tenant server proves it needs it.
func (s *Service) rootRegistry(path string, mtime time.Time) (*resolved, error) {
	s.mu.Lock()
	if c := s.regs[path]; c != nil && c.mtime.Equal(mtime) {
		s.mu.Unlock()
		return c.res, nil
	}
	s.mu.Unlock()

	cfg, err := config.Load(path)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", path, err)
	}
	reg, err := source.NewRegistry(cfg)
	if err != nil {
		return nil, fmt.Errorf("build registry for %s: %w", path, err)
	}
	res := &resolved{reg: reg, queryTimeout: time.Duration(cfg.QueryTimeoutSeconds) * time.Second}

	s.mu.Lock()
	if c := s.regs[path]; c != nil && c.mtime.Equal(mtime) {
		s.mu.Unlock()
		reg.Close() // lost a race; drop our duplicate pools, reuse the winner
		return c.res, nil
	}
	old := s.regs[path]
	s.regs[path] = &cachedReg{mtime: mtime, res: res}
	s.mu.Unlock()
	if old != nil {
		old.res.reg.Close() // config changed on disk — retire the stale pools
	}
	return res, nil
}

// onRootsChanged invalidates the cached roots for the signalling client.
func (s *Service) onRootsChanged(_ context.Context, req *mcp.RootsListChangedRequest) {
	if req.Session == nil {
		return
	}
	s.mu.Lock()
	st := s.sessions[req.Session.ID()]
	s.mu.Unlock()
	if st != nil {
		st.invalidate()
	}
}

// sweepSessions drops registry entries whose SDK session has ended so a
// long-lived server does not leak sessionStates.
func (s *Service) sweepSessions() {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for range t.C {
		live := map[string]struct{}{}
		for ss := range s.srv.Sessions() {
			live[ss.ID()] = struct{}{}
		}
		s.mu.Lock()
		for id := range s.sessions {
			if _, ok := live[id]; !ok {
				delete(s.sessions, id)
			}
		}
		s.mu.Unlock()
	}
}

// isReadonly reports whether the source must be treated as read-only.
func (s *Service) isReadonly(src *source.Source) bool {
	return s.readonlyOverride || src.Readonly()
}

func (s *Service) register(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_sources",
		Description: "List the configured database sources, including their engine, an optional human-readable description of what each source is for (use it to pick the right source for a task), whether they are remote (SSH-tunneled) and whether they are read-only.",
	}, s.listSources)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_databases",
		Description: "List databases/schemas on a source. Argument: source (the source name from list_sources).",
	}, s.listDatabases)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_tables",
		Description: "List tables in a database. Arguments: source (required); database (optional, defaults to the source's configured database).",
	}, s.listTables)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "describe_table",
		Description: "Describe a table's columns. Arguments: source (required); table (required); database (optional).",
	}, s.describeTable)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "read_query",
		Description: "Run a read-only query (SELECT/SHOW/DESCRIBE/EXPLAIN) and return the rows. Arguments: source (required); sql (required); limit (optional, max rows to return, default 1000). Non-read statements are rejected. The result's truncated flag indicates more rows were available.",
	}, s.readQuery)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "write_query",
		Description: "Run a write or DDL statement (INSERT/UPDATE/DELETE/CREATE/ALTER/...). Arguments: source (required); sql (required). Rejected when the source or the server is read-only.",
	}, s.writeQuery)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "explain_query",
		Description: "Return the query plan for a statement using EXPLAIN, without executing it. Arguments: source (required); sql (required).",
	}, s.explainQuery)
}

// --- tool inputs/outputs ---

type sourceInput struct {
	Source string `json:"source"`
}

type queryInput struct {
	Source string `json:"source"`
	SQL    string `json:"sql"`
	Limit  int    `json:"limit,omitempty"`
}

type tablesInput struct {
	Source   string `json:"source"`
	Database string `json:"database,omitempty"`
}

type describeInput struct {
	Source   string `json:"source"`
	Database string `json:"database,omitempty"`
	Table    string `json:"table"`
}

type sourceInfo struct {
	Name        string `json:"name"`
	Engine      string `json:"engine"`
	Description string `json:"description,omitempty"`
	Remote      bool   `json:"remote"`
	Readonly    bool   `json:"readonly"`
}

type sourcesOutput struct {
	Sources []sourceInfo `json:"sources"`
}

type namesOutput struct {
	Values []string `json:"values"`
}

// --- handlers ---

func (s *Service) listSources(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, sourcesOutput, error) {
	r, err := s.resolve(ctx, req)
	if err != nil {
		return errorReply[sourcesOutput]("%v", err)
	}
	out := sourcesOutput{}
	for _, src := range r.reg.List() {
		out.Sources = append(out.Sources, sourceInfo{
			Name:        src.Name(),
			Engine:      src.EngineName(),
			Description: src.Description(),
			Remote:      src.Remote(),
			Readonly:    s.isReadonly(src),
		})
	}
	return reply(out)
}

func (s *Service) listDatabases(ctx context.Context, req *mcp.CallToolRequest, in sourceInput) (*mcp.CallToolResult, namesOutput, error) {
	r, err := s.resolve(ctx, req)
	if err != nil {
		return errorReply[namesOutput]("%v", err)
	}
	src, err := r.reg.Get(in.Source)
	if err != nil {
		return errorReply[namesOutput]("%v", err)
	}
	ctx, cancel := withTimeout(ctx, r.queryTimeout)
	defer cancel()
	values, err := src.QueryColumn(ctx, src.Engine().ListDatabases().SQL)
	if err != nil {
		return errorReply[namesOutput]("list databases: %v", err)
	}
	return reply(namesOutput{Values: values})
}

func (s *Service) listTables(ctx context.Context, req *mcp.CallToolRequest, in tablesInput) (*mcp.CallToolResult, *source.ResultSet, error) {
	r, err := s.resolve(ctx, req)
	if err != nil {
		return errorReply[*source.ResultSet]("%v", err)
	}
	src, err := r.reg.Get(in.Source)
	if err != nil {
		return errorReply[*source.ResultSet]("%v", err)
	}
	ctx, cancel := withTimeout(ctx, r.queryTimeout)
	defer cancel()
	rs, err := src.RunQuery(ctx, src.Engine().ListTables(in.Database), defaultRowLimit)
	if err != nil {
		return errorReply[*source.ResultSet]("list tables: %v", err)
	}
	return reply(rs)
}

func (s *Service) describeTable(ctx context.Context, req *mcp.CallToolRequest, in describeInput) (*mcp.CallToolResult, *source.ResultSet, error) {
	if in.Table == "" {
		return errorReply[*source.ResultSet]("table is required")
	}
	r, err := s.resolve(ctx, req)
	if err != nil {
		return errorReply[*source.ResultSet]("%v", err)
	}
	src, err := r.reg.Get(in.Source)
	if err != nil {
		return errorReply[*source.ResultSet]("%v", err)
	}
	ctx, cancel := withTimeout(ctx, r.queryTimeout)
	defer cancel()
	rs, err := src.RunQuery(ctx, src.Engine().DescribeTable(in.Database, in.Table), defaultRowLimit)
	if err != nil {
		return errorReply[*source.ResultSet]("describe table: %v", err)
	}
	return reply(rs)
}

func (s *Service) readQuery(ctx context.Context, req *mcp.CallToolRequest, in queryInput) (*mcp.CallToolResult, *source.ResultSet, error) {
	r, err := s.resolve(ctx, req)
	if err != nil {
		return errorReply[*source.ResultSet]("%v", err)
	}
	src, err := r.reg.Get(in.Source)
	if err != nil {
		return errorReply[*source.ResultSet]("%v", err)
	}
	if err := sqlguard.EnsureReadOnly(in.SQL); err != nil {
		return errorReply[*source.ResultSet]("%v", err)
	}
	limit := in.Limit
	if limit <= 0 {
		limit = defaultRowLimit
	}
	ctx, cancel := withTimeout(ctx, r.queryTimeout)
	defer cancel()
	rs, err := src.RunQuery(ctx, source.RawQuery(in.SQL), limit)
	if err != nil {
		return errorReply[*source.ResultSet]("query failed: %v", err)
	}
	return reply(rs)
}

func (s *Service) writeQuery(ctx context.Context, req *mcp.CallToolRequest, in queryInput) (*mcp.CallToolResult, *source.ExecResult, error) {
	r, err := s.resolve(ctx, req)
	if err != nil {
		return errorReply[*source.ExecResult]("%v", err)
	}
	src, err := r.reg.Get(in.Source)
	if err != nil {
		return errorReply[*source.ExecResult]("%v", err)
	}
	if s.isReadonly(src) {
		return errorReply[*source.ExecResult]("source %q is read-only; write_query is not permitted", in.Source)
	}
	ctx, cancel := withTimeout(ctx, r.queryTimeout)
	defer cancel()
	res, err := src.RunExec(ctx, in.SQL)
	if err != nil {
		return errorReply[*source.ExecResult]("write failed: %v", err)
	}
	return reply(res)
}

func (s *Service) explainQuery(ctx context.Context, req *mcp.CallToolRequest, in queryInput) (*mcp.CallToolResult, *source.ResultSet, error) {
	r, err := s.resolve(ctx, req)
	if err != nil {
		return errorReply[*source.ResultSet]("%v", err)
	}
	src, err := r.reg.Get(in.Source)
	if err != nil {
		return errorReply[*source.ResultSet]("%v", err)
	}
	ctx, cancel := withTimeout(ctx, r.queryTimeout)
	defer cancel()
	rs, err := src.RunQuery(ctx, source.RawQuery("EXPLAIN "+in.SQL), defaultRowLimit)
	if err != nil {
		return errorReply[*source.ResultSet]("explain failed: %v", err)
	}
	return reply(rs)
}

// --- helpers ---

// reply marshals out as indented JSON for the text content and also returns it
// as the structured tool result.
func reply[T any](out T) (*mcp.CallToolResult, T, error) {
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return errorReply[T]("marshal result: %v", err)
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(b)}},
	}, out, nil
}

// errorReply returns a tool result flagged as an error so the model sees the
// message, without failing the JSON-RPC call itself.
func errorReply[T any](format string, args ...any) (*mcp.CallToolResult, T, error) {
	var zero T
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(format, args...)}},
	}, zero, nil
}
