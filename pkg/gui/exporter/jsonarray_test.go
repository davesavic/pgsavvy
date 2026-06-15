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
