package exporter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/stretchr/testify/require"
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

func TestNDJSON_JSONBColumn_EmbedsAsJSON(t *testing.T) {
	cols := []models.ColumnMeta{{Name: "id", TypeName: "int4"}, {Name: "data", TypeName: "jsonb"}}
	f := NewNDJSON()
	var buf bytes.Buffer
	if err := f.Header(cols, &buf); err != nil {
		t.Fatalf("Header: %v", err)
	}
	if err := f.Row(models.Row{Values: []any{1, []byte(`{"plan":"pro"}`)}}, &buf); err != nil {
		t.Fatalf("Row: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, `"data":"`) {
		t.Fatalf("jsonb embedded as base64 string instead of JSON object: %q", out)
	}
	if !strings.Contains(out, `"data":{"plan":"pro"}`) {
		t.Fatalf("expected embedded JSON object, got %q", out)
	}
}

func TestReview_json_scalar_export(t *testing.T) {
	col := models.ColumnMeta{Name: "value", TypeName: "jsonb"}
	for _, v := range []any{"", "null", "true", "hello", `{"a":1}`, `[1]`, json.RawMessage(`{"a":1}`), []byte(`[1]`), nil} {
		t.Run(fmt.Sprintf("%T/%v", v, v), func(t *testing.T) {
			f := NewNDJSON()
			var buf bytes.Buffer
			require.NoError(t, f.Header([]models.ColumnMeta{col}, &buf))
			require.NoError(t, f.Row(models.Row{Values: []any{v}}, &buf))
			require.NoError(t, f.Footer(&buf))
			var got map[string]any
			require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
			want := v
			switch raw := v.(type) {
			case json.RawMessage:
				require.NoError(t, json.Unmarshal(raw, &want))
			case []byte:
				require.NoError(t, json.Unmarshal(raw, &want))
			}
			require.Equal(t, want, got["value"])
		})
	}
	f := NewNDJSON()
	var buf bytes.Buffer
	require.NoError(t, f.Header([]models.ColumnMeta{col}, &buf))
	require.Error(t, f.Row(models.Row{Values: []any{json.RawMessage("broken")}}, &buf))
}
