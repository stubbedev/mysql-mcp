package source

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/abs/mysql-mcp/internal/engine"
)

// RawQuery wraps a bare SQL string (no positional args) as an engine.Query.
func RawQuery(sql string) engine.Query { return engine.Query{SQL: sql} }

// ResultSet is a generic, JSON-friendly query result.
type ResultSet struct {
	Columns  []string `json:"columns"`
	Rows     [][]any  `json:"rows"`
	RowCount int      `json:"row_count"`
}

// ExecResult reports the outcome of a non-query statement.
type ExecResult struct {
	RowsAffected int64 `json:"rows_affected"`
	LastInsertID int64 `json:"last_insert_id"`
}

// RunQuery executes a read query and collects all rows. Column values that come
// back as raw bytes are converted to strings so the JSON output is readable.
func (s *Source) RunQuery(ctx context.Context, q engine.Query) (*ResultSet, error) {
	db, err := s.DB()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, q.SQL, q.Args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	rs := &ResultSet{Columns: cols, Rows: [][]any{}}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		for i, v := range vals {
			if b, ok := v.([]byte); ok {
				vals[i] = string(b)
			}
		}
		rs.Rows = append(rs.Rows, vals)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rs.RowCount = len(rs.Rows)
	return rs, nil
}

// RunExec executes a write/DDL statement.
func (s *Source) RunExec(ctx context.Context, query string) (*ExecResult, error) {
	db, err := s.DB()
	if err != nil {
		return nil, err
	}
	res, err := db.ExecContext(ctx, query)
	if err != nil {
		return nil, err
	}
	out := &ExecResult{}
	out.RowsAffected, _ = res.RowsAffected()
	out.LastInsertID, _ = res.LastInsertId()
	return out, nil
}

// QueryColumn runs a query expected to yield a single column and returns the
// values as strings. Used by introspection helpers like list_databases.
func (s *Source) QueryColumn(ctx context.Context, query string) ([]string, error) {
	db, err := s.DB()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var v sql.NullString
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, v.String)
	}
	return out, rows.Err()
}
