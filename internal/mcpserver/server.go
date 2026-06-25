// Package mcpserver wires the source registry into an MCP server, exposing a
// small set of tools for inspecting and querying the configured databases.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stubbedev/mysql-mcp/internal/source"
	"github.com/stubbedev/mysql-mcp/internal/sqlguard"
)

// Service holds the dependencies shared by all tool handlers.
type Service struct {
	reg              *source.Registry
	readonlyOverride bool
}

// New builds an MCP server exposing the database tools. readonlyOverride forces
// every source to behave as read-only regardless of its config (the global
// --read-only flag).
func New(reg *source.Registry, version string, readonlyOverride bool) *mcp.Server {
	svc := &Service{reg: reg, readonlyOverride: readonlyOverride}
	srv := mcp.NewServer(&mcp.Implementation{Name: "mysql-mcp", Version: version}, nil)
	svc.register(srv)
	return srv
}

// isReadonly reports whether the source must be treated as read-only.
func (s *Service) isReadonly(src *source.Source) bool {
	return s.readonlyOverride || src.Readonly()
}

func (s *Service) register(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_sources",
		Description: "List the configured database sources, including their engine, whether they are remote (SSH-tunneled) and whether they are read-only.",
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
		Description: "Run a read-only query (SELECT/SHOW/DESCRIBE/EXPLAIN) and return the rows. Arguments: source (required); sql (required). Non-read statements are rejected.",
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
	Name     string `json:"name"`
	Engine   string `json:"engine"`
	Remote   bool   `json:"remote"`
	Readonly bool   `json:"readonly"`
}

type sourcesOutput struct {
	Sources []sourceInfo `json:"sources"`
}

type namesOutput struct {
	Values []string `json:"values"`
}

// --- handlers ---

func (s *Service) listSources(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, sourcesOutput, error) {
	out := sourcesOutput{}
	for _, src := range s.reg.List() {
		out.Sources = append(out.Sources, sourceInfo{
			Name:     src.Name(),
			Engine:   src.EngineName(),
			Remote:   src.Remote(),
			Readonly: s.isReadonly(src),
		})
	}
	return reply(out)
}

func (s *Service) listDatabases(ctx context.Context, _ *mcp.CallToolRequest, in sourceInput) (*mcp.CallToolResult, namesOutput, error) {
	src, err := s.reg.Get(in.Source)
	if err != nil {
		return errorReply[namesOutput]("%v", err)
	}
	values, err := src.QueryColumn(ctx, src.Engine().ListDatabases().SQL)
	if err != nil {
		return errorReply[namesOutput]("list databases: %v", err)
	}
	return reply(namesOutput{Values: values})
}

func (s *Service) listTables(ctx context.Context, _ *mcp.CallToolRequest, in tablesInput) (*mcp.CallToolResult, *source.ResultSet, error) {
	src, err := s.reg.Get(in.Source)
	if err != nil {
		return errorReply[*source.ResultSet]("%v", err)
	}
	rs, err := src.RunQuery(ctx, src.Engine().ListTables(in.Database))
	if err != nil {
		return errorReply[*source.ResultSet]("list tables: %v", err)
	}
	return reply(rs)
}

func (s *Service) describeTable(ctx context.Context, _ *mcp.CallToolRequest, in describeInput) (*mcp.CallToolResult, *source.ResultSet, error) {
	if in.Table == "" {
		return errorReply[*source.ResultSet]("table is required")
	}
	src, err := s.reg.Get(in.Source)
	if err != nil {
		return errorReply[*source.ResultSet]("%v", err)
	}
	rs, err := src.RunQuery(ctx, src.Engine().DescribeTable(in.Database, in.Table))
	if err != nil {
		return errorReply[*source.ResultSet]("describe table: %v", err)
	}
	return reply(rs)
}

func (s *Service) readQuery(ctx context.Context, _ *mcp.CallToolRequest, in queryInput) (*mcp.CallToolResult, *source.ResultSet, error) {
	src, err := s.reg.Get(in.Source)
	if err != nil {
		return errorReply[*source.ResultSet]("%v", err)
	}
	if err := sqlguard.EnsureReadOnly(in.SQL); err != nil {
		return errorReply[*source.ResultSet]("%v", err)
	}
	rs, err := src.RunQuery(ctx, source.RawQuery(in.SQL))
	if err != nil {
		return errorReply[*source.ResultSet]("query failed: %v", err)
	}
	return reply(rs)
}

func (s *Service) writeQuery(ctx context.Context, _ *mcp.CallToolRequest, in queryInput) (*mcp.CallToolResult, *source.ExecResult, error) {
	src, err := s.reg.Get(in.Source)
	if err != nil {
		return errorReply[*source.ExecResult]("%v", err)
	}
	if s.isReadonly(src) {
		return errorReply[*source.ExecResult]("source %q is read-only; write_query is not permitted", in.Source)
	}
	res, err := src.RunExec(ctx, in.SQL)
	if err != nil {
		return errorReply[*source.ExecResult]("write failed: %v", err)
	}
	return reply(res)
}

func (s *Service) explainQuery(ctx context.Context, _ *mcp.CallToolRequest, in queryInput) (*mcp.CallToolResult, *source.ResultSet, error) {
	src, err := s.reg.Get(in.Source)
	if err != nil {
		return errorReply[*source.ResultSet]("%v", err)
	}
	rs, err := src.RunQuery(ctx, source.RawQuery("EXPLAIN "+in.SQL))
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
