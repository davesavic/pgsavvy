package exporter

import (
	"bytes"
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/models"
)

func markdownCols() []models.ColumnMeta {
	return []models.ColumnMeta{
		{Name: "id"},
		{Name: "name"},
	}
}

func TestMarkdown_TableShape(t *testing.T) {
	f := NewMarkdown()
	var buf bytes.Buffer
	if err := f.Header(markdownCols(), &buf); err != nil {
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
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines (header + sep + 2 data), got %d: %q", len(lines), buf.String())
	}
	if lines[0] != "|id|name|" {
		t.Fatalf("header line = %q, want %q", lines[0], "|id|name|")
	}
	if lines[1] != "|---|---|" {
		t.Fatalf("separator line = %q, want %q", lines[1], "|---|---|")
	}
	if lines[2] != "|1|alice|" {
		t.Fatalf("data line 1 = %q, want %q", lines[2], "|1|alice|")
	}
	if lines[3] != "|2|bob|" {
		t.Fatalf("data line 2 = %q, want %q", lines[3], "|2|bob|")
	}
}

func TestMarkdown_EscapesPipe(t *testing.T) {
	f := NewMarkdown()
	var buf bytes.Buffer
	if err := f.Header(markdownCols(), &buf); err != nil {
		t.Fatalf("Header: %v", err)
	}
	if err := f.Row(models.Row{Values: []any{1, "a|b"}}, &buf); err != nil {
		t.Fatalf("Row: %v", err)
	}
	if err := f.Footer(&buf); err != nil {
		t.Fatalf("Footer: %v", err)
	}
	if !strings.Contains(buf.String(), `a\|b`) {
		t.Fatalf("expected escaped pipe a\\|b in %q", buf.String())
	}
}

func TestMarkdown_NewlineReplaced(t *testing.T) {
	f := NewMarkdown()
	var buf bytes.Buffer
	if err := f.Header(markdownCols(), &buf); err != nil {
		t.Fatalf("Header: %v", err)
	}
	if err := f.Row(models.Row{Values: []any{1, "a\nb"}}, &buf); err != nil {
		t.Fatalf("Row: %v", err)
	}
	if err := f.Footer(&buf); err != nil {
		t.Fatalf("Footer: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "|a b|") {
		t.Fatalf("expected newline replaced with single space in %q", out)
	}
	// Ensure the embedded \n didn't leak into the table body (only trailing \n per line).
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %q", len(lines), out)
	}
}

func TestMarkdown_NULL_RendersAsEmpty(t *testing.T) {
	f := NewMarkdown()
	var buf bytes.Buffer
	if err := f.Header(markdownCols(), &buf); err != nil {
		t.Fatalf("Header: %v", err)
	}
	if err := f.Row(models.Row{Values: []any{1, nil}}, &buf); err != nil {
		t.Fatalf("Row: %v", err)
	}
	if err := f.Footer(&buf); err != nil {
		t.Fatalf("Footer: %v", err)
	}
	if !strings.Contains(buf.String(), "|1||\n") {
		t.Fatalf("expected NULL to render as empty cell in %q", buf.String())
	}
}

func TestMarkdown_BuffersUntilFooter(t *testing.T) {
	f := NewMarkdown()
	var buf bytes.Buffer
	if err := f.Header(markdownCols(), &buf); err != nil {
		t.Fatalf("Header: %v", err)
	}
	if err := f.Row(models.Row{Values: []any{1, "x"}}, &buf); err != nil {
		t.Fatalf("Row: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected no output until Footer, got %q", buf.String())
	}
	if err := f.Footer(&buf); err != nil {
		t.Fatalf("Footer: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("expected Footer to flush buffered output")
	}
}

func TestMarkdown_SanitizesEscapeSequences(t *testing.T) {
	f := NewMarkdown()
	var buf bytes.Buffer
	if err := f.Header(markdownCols(), &buf); err != nil {
		t.Fatalf("Header: %v", err)
	}
	// Embed an ESC byte; SanitizeCellEscapes should strip/neutralize it.
	if err := f.Row(models.Row{Values: []any{1, "a\x1b[31mb"}}, &buf); err != nil {
		t.Fatalf("Row: %v", err)
	}
	if err := f.Footer(&buf); err != nil {
		t.Fatalf("Footer: %v", err)
	}
	if strings.ContainsRune(buf.String(), '\x1b') {
		t.Fatalf("raw ESC byte leaked through to output: %q", buf.String())
	}
}

func TestMarkdown_IsStreamingFalse(t *testing.T) {
	if NewMarkdown().IsStreaming() {
		t.Fatal("expected Markdown to report IsStreaming()=false")
	}
}
