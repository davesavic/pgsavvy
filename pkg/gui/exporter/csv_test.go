package exporter

import (
	"bytes"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/models"
)

func csvCols() []models.ColumnMeta {
	return []models.ColumnMeta{{Name: "id"}, {Name: "name"}}
}

func reviewSerializeCases() []struct {
	name  string
	value any
	col   models.ColumnMeta
	want  string
} {
	long := strings.Repeat("x", 12000)
	raw := `{"body":"` + long + `"}`
	quoted, _ := json.Marshal(long)
	data := bytes.Repeat([]byte{0, 1, 0xff}, 4096)
	return []struct {
		name  string
		value any
		col   models.ColumnMeta
		want  string
	}{
		{"text", long, models.ColumnMeta{TypeName: "text"}, long},
		{"unicode", strings.Repeat("雪", 4000), models.ColumnMeta{TypeName: "text"}, strings.Repeat("雪", 4000)},
		{"json_raw", []byte(raw), models.ColumnMeta{TypeName: "jsonb"}, raw},
		{"json_raw_oid", json.RawMessage(raw), models.ColumnMeta{TypeOID: 3802}, raw},
		{"json_decoded_string", long, models.ColumnMeta{TypeName: "json"}, string(quoted)},
		{"json_decoded_object", map[string]any{"body": long}, models.ColumnMeta{TypeName: "jsonb"}, raw},
		{"bytea_empty", []byte{}, models.ColumnMeta{TypeName: "bytea"}, `\x`},
		{"bytea_eight", []byte("12345678"), models.ColumnMeta{TypeName: "bytea"}, `\x3132333435363738`},
		{"bytea_nine_oid", []byte("123456789"), models.ColumnMeta{TypeOID: 17}, `\x313233343536373839`},
		{"bytea_large", data, models.ColumnMeta{TypeName: "bytea"}, `\x` + hex.EncodeToString(data)},
		{"bytea_non_slice_sanitized", "a\rb\x1b[31mEND\x1b[0m", models.ColumnMeta{TypeName: "bytea"}, "abEND"},
		{"null", nil, models.ColumnMeta{TypeName: "text"}, ""},
		{"empty", "", models.ColumnMeta{TypeName: "text"}, ""},
		{"delimiters", ",\"quoted\"\tpipe|\nreturn\rend", models.ColumnMeta{TypeName: "text"}, ",\"quoted\"\tpipe|\nreturnend"},
		{"escape_after_display_cap", long + "\x1b[31mEND\x1b[0m", models.ColumnMeta{TypeName: "text"}, long + "END"},
		{"already_retained_prefix", "retained prefix…", models.ColumnMeta{TypeName: "text"}, "retained prefix…"},
	}
}

func reviewSerializeDelimited(t *testing.T, factory func() Format, delimiter rune) {
	t.Helper()
	for _, tc := range reviewSerializeCases() {
		t.Run(tc.name, func(t *testing.T) {
			f := factory()
			var buf bytes.Buffer
			if err := f.Header([]models.ColumnMeta{tc.col, {Name: "guard"}}, &buf); err != nil {
				t.Fatal(err)
			}
			if err := f.Row(models.Row{Values: []any{tc.value, "guard"}}, &buf); err != nil {
				t.Fatal(err)
			}
			if err := f.Footer(&buf); err != nil {
				t.Fatal(err)
			}
			reader := csv.NewReader(&buf)
			reader.Comma = delimiter
			records, err := reader.ReadAll()
			if err != nil {
				t.Fatal(err)
			}
			if len(records) != 2 || len(records[1]) != 2 || records[1][1] != "guard" {
				t.Fatalf("unexpected shape %v", records)
			}
			if got := records[1][0]; got != tc.want {
				t.Fatalf("retained export differs: got %d bytes, want %d bytes", len(got), len(tc.want))
			}
		})
	}
}

func TestReview_serialize_csv(t *testing.T) { reviewSerializeDelimited(t, NewCSV, ',') }

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
