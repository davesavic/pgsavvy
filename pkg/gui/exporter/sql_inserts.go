package exporter

import (
	"fmt"
	"io"
	"strings"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// sqlInsertsFormat implements Format by emitting one INSERT statement per row.
//
// Security boundary: per-value SQL safety is delegated entirely to the
// drivers.Encoder (e.g. the pg encoder emits E'…' literals with full backslash
// + apostrophe escaping). We therefore do NOT additionally route values
// through grid.SanitizeCellEscapes — the encoder is the single, type-aware
// escape layer. Identifier quoting (column + table names) is handled locally
// with PostgreSQL standard double-quoting.
type sqlInsertsFormat struct {
	baseTable string
	encoder   drivers.Encoder
	cols      []models.ColumnMeta
}

// NewSQLInserts returns a Format that emits one INSERT per row targeting
// baseTable. baseTable may be bare ("users") or schema-qualified
// ("public.users"); each component is double-quoted. encoder produces the
// per-value SQL literals and is responsible for escape-safety.
func NewSQLInserts(baseTable string, encoder drivers.Encoder) Format {
	return &sqlInsertsFormat{baseTable: baseTable, encoder: encoder}
}

func (s *sqlInsertsFormat) Name() string      { return "SQL INSERTs" }
func (s *sqlInsertsFormat) Ext() string       { return "sql" }
func (s *sqlInsertsFormat) IsStreaming() bool { return true }

func (s *sqlInsertsFormat) Header(cols []models.ColumnMeta, w io.Writer) error {
	s.cols = cols
	if _, err := io.WriteString(w, "SET standard_conforming_strings = on;\n"); err != nil {
		return err
	}
	return nil
}

func (s *sqlInsertsFormat) Row(r models.Row, w io.Writer) error {
	if len(s.cols) == 0 {
		return fmt.Errorf("exporter/sql_inserts: Row called before Header")
	}
	if s.encoder == nil {
		return fmt.Errorf("exporter/sql_inserts: nil encoder")
	}
	var b strings.Builder
	b.WriteString("INSERT INTO ")
	b.WriteString(pgQuoteQualified(s.baseTable))
	b.WriteString(" (")
	for i, c := range s.cols {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(pgQuoteIdent(c.Name))
	}
	b.WriteString(") VALUES (")
	for i, c := range s.cols {
		if i > 0 {
			b.WriteString(", ")
		}
		var v any
		if i < len(r.Values) {
			v = r.Values[i]
		}
		b.WriteString(s.encoder.EncodeLiteral(v, c.TypeOID))
	}
	b.WriteString(");\n")
	_, err := io.WriteString(w, b.String())
	return err
}

func (s *sqlInsertsFormat) Footer(w io.Writer) error { return nil }

// pgQuoteIdent wraps a PostgreSQL identifier in double quotes, doubling any
// embedded double quotes per the SQL standard.
func pgQuoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// pgQuoteQualified splits a possibly schema-qualified name on the first '.'
// and double-quotes each component. Bare names are quoted as a single ident.
func pgQuoteQualified(s string) string {
	parts := strings.SplitN(s, ".", 2)
	if len(parts) == 2 {
		return pgQuoteIdent(parts[0]) + "." + pgQuoteIdent(parts[1])
	}
	return pgQuoteIdent(s)
}
