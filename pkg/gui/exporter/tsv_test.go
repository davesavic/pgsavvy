package exporter

import (
	"bytes"
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/models"
)

func TestTSV_HeaderAndRows_NoQuotingNeeded(t *testing.T) {
	f := NewTSV()
	var buf bytes.Buffer
	cols := []models.ColumnMeta{{Name: "id"}, {Name: "name"}}
	if err := f.Header(cols, &buf); err != nil {
		t.Fatalf("Header: %v", err)
	}
	if err := f.Row(models.Row{Values: []any{1, "alice"}}, &buf); err != nil {
		t.Fatalf("Row: %v", err)
	}
	got := buf.String()
	want := "id\tname\r\n1\talice\r\n"
	if got != want {
		t.Fatalf("TSV mismatch:\n got=%q\nwant=%q", got, want)
	}
}

func TestTSV_QuotesFieldsContainingTab(t *testing.T) {
	f := NewTSV()
	var buf bytes.Buffer
	if err := f.Row(models.Row{Values: []any{"a\tb"}}, &buf); err != nil {
		t.Fatalf("Row: %v", err)
	}
	got := buf.String()
	want := "\"a\tb\"\r\n"
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func TestTSV_QuotesFieldsContainingNewline(t *testing.T) {
	f := NewTSV()
	var buf bytes.Buffer
	if err := f.Row(models.Row{Values: []any{"a\nb"}}, &buf); err != nil {
		t.Fatalf("Row: %v", err)
	}
	got := buf.String()
	want := "\"a\nb\"\r\n"
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func TestTSV_NULL_RendersAsEmpty(t *testing.T) {
	f := NewTSV()
	var buf bytes.Buffer
	if err := f.Row(models.Row{Values: []any{1, nil}}, &buf); err != nil {
		t.Fatalf("Row: %v", err)
	}
	got := buf.String()
	want := "1\t\r\n"
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func TestTSV_SanitizesEscapeSequences(t *testing.T) {
	f := NewTSV()
	var buf bytes.Buffer
	if err := f.Row(models.Row{Values: []any{"\x1b]0;evil\x07"}}, &buf); err != nil {
		t.Fatalf("Row: %v", err)
	}
	out := buf.String()
	if strings.ContainsAny(out, "\x1b\x07") {
		t.Fatalf("escape bytes leaked through sanitizer: %q", out)
	}
}
