package exporter

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/models"
)

func jsonArrayCols() []models.ColumnMeta {
	return []models.ColumnMeta{
		{Name: "id"},
		{Name: "name"},
	}
}

func TestJSONArray_BuffersUntilFooter(t *testing.T) {
	f := NewJSONArray()
	var buf bytes.Buffer
	if err := f.Header(jsonArrayCols(), &buf); err != nil {
		t.Fatalf("Header: %v", err)
	}
	if err := f.Row(models.Row{Values: []any{1, "alice"}}, &buf); err != nil {
		t.Fatalf("Row: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected no output until Footer, got %q", buf.String())
	}
	if err := f.Footer(&buf); err != nil {
		t.Fatalf("Footer: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("expected Footer to emit output")
	}
	// Should be a valid JSON array.
	var arr []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &arr); err != nil {
		t.Fatalf("output not valid JSON: %v (%q)", err, buf.String())
	}
	if len(arr) != 1 {
		t.Fatalf("expected 1 element, got %d", len(arr))
	}
}

func TestJSONArray_EmptyResult_EmitsEmptyArray(t *testing.T) {
	f := NewJSONArray()
	var buf bytes.Buffer
	if err := f.Header(jsonArrayCols(), &buf); err != nil {
		t.Fatalf("Header: %v", err)
	}
	if err := f.Footer(&buf); err != nil {
		t.Fatalf("Footer: %v", err)
	}
	if got := buf.String(); got != "[]\n" {
		t.Fatalf("expected %q, got %q", "[]\n", got)
	}
}

func TestJSONArray_NULL_BecomesJSONNull(t *testing.T) {
	f := NewJSONArray()
	var buf bytes.Buffer
	if err := f.Header(jsonArrayCols(), &buf); err != nil {
		t.Fatalf("Header: %v", err)
	}
	if err := f.Row(models.Row{Values: []any{1, nil}}, &buf); err != nil {
		t.Fatalf("Row: %v", err)
	}
	if err := f.Footer(&buf); err != nil {
		t.Fatalf("Footer: %v", err)
	}
	if !strings.Contains(buf.String(), `"name":null`) {
		t.Fatalf("expected literal JSON null, got %q", buf.String())
	}
}

func TestJSONArray_HTML_Chars_NotEscaped(t *testing.T) {
	f := NewJSONArray()
	var buf bytes.Buffer
	if err := f.Header(jsonArrayCols(), &buf); err != nil {
		t.Fatalf("Header: %v", err)
	}
	if err := f.Row(models.Row{Values: []any{1, "<a&b>"}}, &buf); err != nil {
		t.Fatalf("Row: %v", err)
	}
	if err := f.Footer(&buf); err != nil {
		t.Fatalf("Footer: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "<a&b>") {
		t.Fatalf("HTML chars should survive un-escaped, got %q", out)
	}
	if strings.Contains(out, "\\u003c") {
		t.Fatalf("found HTML-escaped '<' in output: %q", out)
	}
}

func TestJSONArray_IsStreamingFalse(t *testing.T) {
	if NewJSONArray().IsStreaming() {
		t.Fatal("expected JSON Array to report IsStreaming()=false")
	}
}

func TestJSONArray_JSONBColumn_EmbedsAsJSON(t *testing.T) {
	cols := []models.ColumnMeta{{Name: "id", TypeName: "int4"}, {Name: "data", TypeName: "jsonb"}}
	f := NewJSONArray()
	var buf bytes.Buffer
	if err := f.Header(cols, &buf); err != nil {
		t.Fatalf("Header: %v", err)
	}
	if err := f.Row(models.Row{Values: []any{1, []byte(`{"plan":"pro"}`)}}, &buf); err != nil {
		t.Fatalf("Row: %v", err)
	}
	if err := f.Footer(&buf); err != nil {
		t.Fatalf("Footer: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, `"data":"`) {
		t.Fatalf("jsonb embedded as base64 string, not JSON object: %q", out)
	}
	if !strings.Contains(out, `"data":{"plan":"pro"}`) {
		t.Fatalf("expected embedded JSON object, got %q", out)
	}
}

func TestJSONArray_JSONBByOID_EmbedsAsJSON(t *testing.T) {
	cols := []models.ColumnMeta{{Name: "data", TypeOID: 3802}}
	f := NewJSONArray()
	var buf bytes.Buffer
	if err := f.Header(cols, &buf); err != nil {
		t.Fatalf("Header: %v", err)
	}
	if err := f.Row(models.Row{Values: []any{[]byte(`{"a":1}`)}}, &buf); err != nil {
		t.Fatalf("Row: %v", err)
	}
	if err := f.Footer(&buf); err != nil {
		t.Fatalf("Footer: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `"data":{"a":1}`) {
		t.Fatalf("expected embedded JSON via OID fallback, got %q", out)
	}
}

func TestJSONArray_NonJSONBByteaColumn_NotTouched(t *testing.T) {
	cols := []models.ColumnMeta{{Name: "blob", TypeName: "bytea"}}
	f := NewJSONArray()
	var buf bytes.Buffer
	if err := f.Header(cols, &buf); err != nil {
		t.Fatalf("Header: %v", err)
	}
	if err := f.Row(models.Row{Values: []any{[]byte{0x48, 0x65}}}, &buf); err != nil {
		t.Fatalf("Row: %v", err)
	}
	if err := f.Footer(&buf); err != nil {
		t.Fatalf("Footer: %v", err)
	}
	out := buf.String()
	// Non-jsonb []byte stays as base64 via json.Marshal (default behavior).
	if !strings.Contains(out, `"blob":"`) {
		t.Fatal("non-jsonb []byte should still be string (base64) encoded by json.Marshal")
	}
}
