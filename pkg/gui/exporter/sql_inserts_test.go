package exporter

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// fakeEncoder is a minimal drivers.Encoder for unit tests; no Postgres dep.
type fakeEncoder struct{}

func (fakeEncoder) EncodeLiteral(v any, _ uint32) string {
	if v == nil {
		return "NULL"
	}
	switch x := v.(type) {
	case int, int32, int64:
		return fmt.Sprintf("%d", x)
	case string:
		return "E'" + strings.ReplaceAll(x, `'`, `''`) + "'"
	case bool:
		if x {
			return "true"
		}
		return "false"
	}
	return "<?>"
}

func twoCols() []models.ColumnMeta {
	return []models.ColumnMeta{
		{Name: "id", TypeOID: 23},
		{Name: "name", TypeOID: 25},
	}
}

func TestSQLInserts_HeaderEmitsPrologue(t *testing.T) {
	f := NewSQLInserts("t", fakeEncoder{})
	var buf bytes.Buffer
	if err := f.Header(twoCols(), &buf); err != nil {
		t.Fatalf("Header: %v", err)
	}
	got := buf.String()
	want := "SET standard_conforming_strings = on;\n"
	if got != want {
		t.Fatalf("Header output = %q; want %q", got, want)
	}
}

func TestSQLInserts_RowEmitsInsert(t *testing.T) {
	f := NewSQLInserts("public.users", fakeEncoder{})
	var buf bytes.Buffer
	if err := f.Header(twoCols(), &buf); err != nil {
		t.Fatalf("Header: %v", err)
	}
	buf.Reset()
	if err := f.Row(models.Row{Values: []any{1, "alice"}}, &buf); err != nil {
		t.Fatalf("Row: %v", err)
	}
	want := `INSERT INTO "public"."users" ("id", "name") VALUES (1, E'alice');` + "\n"
	if buf.String() != want {
		t.Fatalf("Row output = %q; want %q", buf.String(), want)
	}
}

func TestSQLInserts_NullValue(t *testing.T) {
	f := NewSQLInserts("public.users", fakeEncoder{})
	var buf bytes.Buffer
	if err := f.Header(twoCols(), &buf); err != nil {
		t.Fatalf("Header: %v", err)
	}
	buf.Reset()
	if err := f.Row(models.Row{Values: []any{1, nil}}, &buf); err != nil {
		t.Fatalf("Row: %v", err)
	}
	if !strings.HasSuffix(buf.String(), ", NULL);\n") {
		t.Fatalf("expected trailing NULL); got %q", buf.String())
	}
}

func TestSQLInserts_QuotedIdentifierWithEmbeddedQuote(t *testing.T) {
	cols := []models.ColumnMeta{
		{Name: `x"y`, TypeOID: 23},
	}
	f := NewSQLInserts("t", fakeEncoder{})
	var buf bytes.Buffer
	if err := f.Header(cols, &buf); err != nil {
		t.Fatalf("Header: %v", err)
	}
	buf.Reset()
	if err := f.Row(models.Row{Values: []any{1}}, &buf); err != nil {
		t.Fatalf("Row: %v", err)
	}
	// embedded " in column name should be doubled to ""
	if !strings.Contains(buf.String(), `("x""y")`) {
		t.Fatalf("expected doubled-quote identifier in %q", buf.String())
	}
}

func TestSQLInserts_BareTable_NoSchema(t *testing.T) {
	f := NewSQLInserts("users", fakeEncoder{})
	var buf bytes.Buffer
	if err := f.Header(twoCols(), &buf); err != nil {
		t.Fatalf("Header: %v", err)
	}
	buf.Reset()
	if err := f.Row(models.Row{Values: []any{1, "a"}}, &buf); err != nil {
		t.Fatalf("Row: %v", err)
	}
	if !strings.HasPrefix(buf.String(), `INSERT INTO "users" (`) {
		t.Fatalf("expected bare-table quoting; got %q", buf.String())
	}
}

func TestSQLInserts_QualifiedTable(t *testing.T) {
	f := NewSQLInserts("myschema.t", fakeEncoder{})
	var buf bytes.Buffer
	if err := f.Header(twoCols(), &buf); err != nil {
		t.Fatalf("Header: %v", err)
	}
	buf.Reset()
	if err := f.Row(models.Row{Values: []any{1, "a"}}, &buf); err != nil {
		t.Fatalf("Row: %v", err)
	}
	if !strings.HasPrefix(buf.String(), `INSERT INTO "myschema"."t" (`) {
		t.Fatalf("expected qualified-table quoting; got %q", buf.String())
	}
}

func TestSQLInserts_RowBeforeHeader_Errors(t *testing.T) {
	f := NewSQLInserts("t", fakeEncoder{})
	var buf bytes.Buffer
	err := f.Row(models.Row{Values: []any{1}}, &buf)
	if err == nil {
		t.Fatalf("expected error when Row called before Header")
	}
}

func TestSQLInserts_NilEncoder_Errors(t *testing.T) {
	f := NewSQLInserts("t", nil)
	var buf bytes.Buffer
	if err := f.Header(twoCols(), &buf); err != nil {
		t.Fatalf("Header: %v", err)
	}
	err := f.Row(models.Row{Values: []any{1, "a"}}, &buf)
	if err == nil {
		t.Fatalf("expected error when encoder is nil")
	}
}

func TestSQLInserts_FooterIsNoop(t *testing.T) {
	f := NewSQLInserts("t", fakeEncoder{})
	var buf bytes.Buffer
	if err := f.Footer(&buf); err != nil {
		t.Fatalf("Footer: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("Footer should be a no-op; wrote %q", buf.String())
	}
}

func TestSQLInserts_Ext_And_IsStreaming(t *testing.T) {
	f := NewSQLInserts("t", fakeEncoder{})
	if f.Ext() != "sql" {
		t.Fatalf("Ext = %q; want %q", f.Ext(), "sql")
	}
	if !f.IsStreaming() {
		t.Fatalf("IsStreaming = false; want true")
	}
	if f.Name() != "SQL INSERTs" {
		t.Fatalf("Name = %q; want %q", f.Name(), "SQL INSERTs")
	}
}
