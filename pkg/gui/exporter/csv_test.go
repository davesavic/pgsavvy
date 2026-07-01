package exporter

import (
	"bytes"
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/models"
)

func csvCols() []models.ColumnMeta {
	return []models.ColumnMeta{{Name: "id"}, {Name: "name"}}
}

func TestCSV_HeaderAndRows_NoQuotingNeeded(t *testing.T) {
	f := NewCSV()
	var buf bytes.Buffer
	if err := f.Header(csvCols(), &buf); err != nil {
		t.Fatalf("Header: %v", err)
	}
	if err := f.Row(models.Row{Values: []any{1, "alice"}}, &buf); err != nil {
		t.Fatalf("Row: %v", err)
	}
	got := buf.String()
	want := "id,name\r\n1,alice\r\n"
	if got != want {
		t.Fatalf("CSV mismatch:\n got=%q\nwant=%q", got, want)
	}
}

func TestCSV_QuotesFieldsContainingDelimiter(t *testing.T) {
	f := NewCSV()
	var buf bytes.Buffer
	if err := f.Row(models.Row{Values: []any{"a,b"}}, &buf); err != nil {
		t.Fatalf("Row: %v", err)
	}
	got := buf.String()
	want := "\"a,b\"\r\n"
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func TestCSV_QuotesFieldsContainingQuote(t *testing.T) {
	f := NewCSV()
	var buf bytes.Buffer
	if err := f.Row(models.Row{Values: []any{`He said "hi".`}}, &buf); err != nil {
		t.Fatalf("Row: %v", err)
	}
	got := buf.String()
	want := "\"He said \"\"hi\"\".\"\r\n"
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func TestCSV_QuotesFieldsContainingNewline(t *testing.T) {
	f := NewCSV()
	var buf bytes.Buffer
	// Bare \n is preserved by SanitizeCellEscapes; CR is stripped as a
	// C0 control, so we test with \n to verify newline-triggered quoting.
	if err := f.Row(models.Row{Values: []any{"a\nb"}}, &buf); err != nil {
		t.Fatalf("Row: %v", err)
	}
	got := buf.String()
	want := "\"a\nb\"\r\n"
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func TestCSV_NULL_RendersAsEmpty(t *testing.T) {
	f := NewCSV()
	var buf bytes.Buffer
	if err := f.Row(models.Row{Values: []any{1, nil}}, &buf); err != nil {
		t.Fatalf("Row: %v", err)
	}
	got := buf.String()
	want := "1,\r\n"
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func TestCSV_SanitizesEscapeSequences(t *testing.T) {
	f := NewCSV()
	var buf bytes.Buffer
	if err := f.Row(models.Row{Values: []any{"\x1b]0;evil\x07"}}, &buf); err != nil {
		t.Fatalf("Row: %v", err)
	}
	out := buf.String()
	if strings.ContainsAny(out, "\x1b\x07") {
		t.Fatalf("escape bytes leaked through sanitizer: %q", out)
	}
}

func TestCSV_FooterIsNoop(t *testing.T) {
	f := NewCSV()
	var buf bytes.Buffer
	if err := f.Footer(&buf); err != nil {
		t.Fatalf("Footer: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("Footer wrote bytes: %q", buf.String())
	}
}

func TestCSV_EmptyResult_HeaderOnly(t *testing.T) {
	f := NewCSV()
	var buf bytes.Buffer
	if err := f.Header(csvCols(), &buf); err != nil {
		t.Fatalf("Header: %v", err)
	}
	if err := f.Footer(&buf); err != nil {
		t.Fatalf("Footer: %v", err)
	}
	got := buf.String()
	want := "id,name\r\n"
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func TestCSV_JSONBColumn_RendersAsJSONText(t *testing.T) {
	cols := []models.ColumnMeta{{Name: "id", TypeName: "int4"}, {Name: "data", TypeName: "jsonb"}}
	f := NewCSV()
	var buf bytes.Buffer
	if err := f.Header(cols, &buf); err != nil {
		t.Fatalf("Header: %v", err)
	}
	if err := f.Row(models.Row{Values: []any{1, []byte(`{"plan":"pro"}`)}}, &buf); err != nil {
		t.Fatalf("Row: %v", err)
	}
	got := buf.String()
	if strings.Contains(got, "[123") {
		t.Fatalf("jsonb rendered as Go byte-array dump, not JSON text: %q", got)
	}
	want := "id,data\r\n1,\"{\"\"plan\"\":\"\"pro\"\"}\"\r\n"
	if got != want {
		t.Fatalf("\n got=%q\nwant=%q", got, want)
	}
}

func TestCSV_JSONBByOID_RendersAsJSONText(t *testing.T) {
	cols := []models.ColumnMeta{{Name: "data", TypeOID: 3802}}
	f := NewCSV()
	var buf bytes.Buffer
	if err := f.Header(cols, &buf); err != nil {
		t.Fatalf("Header: %v", err)
	}
	if err := f.Row(models.Row{Values: []any{[]byte(`{}`)}}, &buf); err != nil {
		t.Fatalf("Row: %v", err)
	}
	got := buf.String()
	if strings.Contains(got, "[123 125]") {
		t.Fatalf("jsonb-by-OID rendered as Go byte-array dump: %q", got)
	}
	want := "data\r\n{}\r\n"
	if got != want {
		t.Fatalf("\n got=%q\nwant=%q", got, want)
	}
}
