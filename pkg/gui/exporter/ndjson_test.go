package exporter

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/models"
)

func ndjsonCols() []models.ColumnMeta {
	return []models.ColumnMeta{
		{Name: "id"},
		{Name: "name"},
	}
}

func TestNDJSON_OneLinePerRow(t *testing.T) {
	f := NewNDJSON()
	var buf bytes.Buffer
	cols := ndjsonCols()
	if err := f.Header(cols, &buf); err != nil {
		t.Fatalf("Header: %v", err)
	}
	if err := f.Row(models.Row{Values: []any{1, "alice"}}, &buf); err != nil {
		t.Fatalf("Row 1: %v", err)
	}
	if err := f.Row(models.Row{Values: []any{2, "bob"}}, &buf); err != nil {
		t.Fatalf("Row 2: %v", err)
	}
	if err := f.Footer(&buf); err != nil {
		t.Fatalf("Footer: %v", err)
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), buf.String())
	}
	for i, line := range lines {
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("line %d not valid JSON: %v (%q)", i, err, line)
		}
		if _, ok := obj["id"]; !ok {
			t.Fatalf("line %d missing 'id' key", i)
		}
		if _, ok := obj["name"]; !ok {
			t.Fatalf("line %d missing 'name' key", i)
		}
	}
}

func TestNDJSON_NULL_BecomesJSONNull(t *testing.T) {
	f := NewNDJSON()
	var buf bytes.Buffer
	if err := f.Header(ndjsonCols(), &buf); err != nil {
		t.Fatalf("Header: %v", err)
	}
	if err := f.Row(models.Row{Values: []any{1, nil}}, &buf); err != nil {
		t.Fatalf("Row: %v", err)
	}
	if err := f.Footer(&buf); err != nil {
		t.Fatalf("Footer: %v", err)
	}
	if !strings.Contains(buf.String(), `"name":null`) {
		t.Fatalf("expected literal JSON null for nil value, got %q", buf.String())
	}
}

func TestNDJSON_HTML_Chars_NotEscaped(t *testing.T) {
	f := NewNDJSON()
	var buf bytes.Buffer
	if err := f.Header(ndjsonCols(), &buf); err != nil {
		t.Fatalf("Header: %v", err)
	}
	if err := f.Row(models.Row{Values: []any{1, "<script>&amp;</script>"}}, &buf); err != nil {
		t.Fatalf("Row: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "<script>") {
		t.Fatalf("'<' should not be HTML-escaped, got %q", out)
	}
	if strings.Contains(out, "\\u003c") {
		t.Fatalf("'<' should not appear as \\u003c, got %q", out)
	}
}

func TestNDJSON_EmptyResult_NoOutput(t *testing.T) {
	f := NewNDJSON()
	var buf bytes.Buffer
	if err := f.Header(ndjsonCols(), &buf); err != nil {
		t.Fatalf("Header: %v", err)
	}
	if err := f.Footer(&buf); err != nil {
		t.Fatalf("Footer: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected empty output, got %q", buf.String())
	}
}

func TestNDJSON_FooterIsNoop(t *testing.T) {
	f := NewNDJSON()
	var buf bytes.Buffer
	if err := f.Header(ndjsonCols(), &buf); err != nil {
		t.Fatalf("Header: %v", err)
	}
	if err := f.Row(models.Row{Values: []any{1, "x"}}, &buf); err != nil {
		t.Fatalf("Row: %v", err)
	}
	before := buf.Len()
	if err := f.Footer(&buf); err != nil {
		t.Fatalf("Footer: %v", err)
	}
	if buf.Len() != before {
		t.Fatalf("Footer should be a no-op; size changed from %d to %d", before, buf.Len())
	}
}

func TestNDJSON_IsStreamingTrue(t *testing.T) {
	if !NewNDJSON().IsStreaming() {
		t.Fatal("expected NDJSON to report IsStreaming()=true")
	}
}
