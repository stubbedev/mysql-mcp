// Package sqlguard classifies SQL statements so the server can enforce
// read-only sources. It parses the statement with a real MySQL parser rather
// than matching on string prefixes, which would be trivial to bypass (e.g.
// leading comments, whitespace or casing).
package sqlguard

import (
	"fmt"

	"github.com/xwb1989/sqlparser"
)

// ReadOnly reports whether sql is a single, pure read statement
// (SELECT/UNION/SHOW/DESCRIBE/EXPLAIN). It returns an error if the statement
// cannot be parsed or if multiple statements are stacked together.
func ReadOnly(sql string) (bool, error) {
	pieces, err := sqlparser.SplitStatementToPieces(sql)
	if err != nil {
		return false, fmt.Errorf("parse sql: %w", err)
	}
	nonEmpty := pieces[:0]
	for _, p := range pieces {
		if p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	if len(nonEmpty) != 1 {
		return false, fmt.Errorf("expected a single statement, got %d", len(nonEmpty))
	}

	stmt, err := sqlparser.Parse(nonEmpty[0])
	if err != nil {
		return false, fmt.Errorf("parse sql: %w", err)
	}
	switch stmt.(type) {
	case *sqlparser.Select, *sqlparser.Union, *sqlparser.Show, *sqlparser.OtherRead:
		// OtherRead covers DESCRIBE and EXPLAIN in this parser.
		return true, nil
	default:
		return false, nil
	}
}

// EnsureReadOnly returns an error if sql is not a pure read statement. Use it to
// gate execution against read-only sources.
func EnsureReadOnly(sql string) error {
	ok, err := ReadOnly(sql)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("statement rejected: source is read-only and this is not a SELECT/SHOW/DESCRIBE/EXPLAIN")
	}
	return nil
}
