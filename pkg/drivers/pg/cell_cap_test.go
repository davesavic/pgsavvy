package pg

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/pgsavvy/pkg/gui/grid"
	"github.com/davesavic/pgsavvy/pkg/models"
)

func TestCapCellValue_SmallValuesUnchanged(t *testing.T) {
	cases := []struct {
		name   string
		val    any
		isJSON bool
	}{
		{"nil", nil, false},
		{"short string", "hello", false},
		{"int", 42, false},
		{"short bytes", []byte("abc"), false},
		{"small json map", map[string]any{"a": 1}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := capCellValue(tc.val, tc.isJSON)
			// json columns normalise maps to RawMessage even when small —
			// that is intended, so assert the rendered text round-trips.
			if tc.name == "small json map" {
				rm, ok := got.(json.RawMessage)
				if !ok {
					t.Fatalf("small json: got %T, want json.RawMessage", got)
				}
				if string(rm) != `{"a":1}` {
					t.Fatalf("small json: got %q, want %q", string(rm), `{"a":1}`)
				}
				return
			}
			if !equalAny(got, tc.val) {
				t.Fatalf("got %#v, want unchanged %#v", got, tc.val)
			}
		})
	}
}

func TestCapCellValue_NilNeverBecomesJSONNull(t *testing.T) {
	// A SQL NULL in a json column must stay nil, not turn into "null".
	if got := capCellValue(nil, true); got != nil {
		t.Fatalf("nil json cell: got %#v, want nil", got)
	}
}

func TestCapCellValue_LargeStringTruncated(t *testing.T) {
	big := strings.Repeat("x", MaxStoredCellBytes*2)
	got, ok := capCellValue(big, false).(string)
	if !ok {
		t.Fatalf("got %T, want string", capCellValue(big, false))
	}
	if len(got) >= len(big) {
		t.Fatalf("not truncated: len=%d, want < %d", len(got), len(big))
	}
	if len(got) > MaxStoredCellBytes+len(cellTruncationMarker) {
		t.Fatalf("over cap: len=%d", len(got))
	}
	if !strings.HasSuffix(got, cellTruncationMarker) {
		t.Fatalf("missing truncation marker: %q", got[len(got)-20:])
	}
}

func TestCapCellValue_LargeBytesTruncatedNoMarker(t *testing.T) {
	big := make([]byte, MaxStoredCellBytes*2)
	got, ok := capCellValue(big, false).([]byte)
	if !ok {
		t.Fatalf("got %T, want []byte", capCellValue(big, false))
	}
	if len(got) > MaxStoredCellBytes {
		t.Fatalf("over cap: len=%d, want <= %d", len(got), MaxStoredCellBytes)
	}
}

func TestCapCellValue_LargeJSONMapNormalisedAndTruncated(t *testing.T) {
	// Build a json map whose marshaled form exceeds the cap.
	big := map[string]any{"payload": strings.Repeat("y", MaxStoredCellBytes*2)}
	got := capCellValue(big, true)
	rm, ok := got.(json.RawMessage)
	if !ok {
		t.Fatalf("got %T, want json.RawMessage", got)
	}
	if len(rm) > MaxStoredCellBytes+len(cellTruncationMarker) {
		t.Fatalf("over cap: len=%d", len(rm))
	}
	if !strings.HasSuffix(string(rm), cellTruncationMarker) {
		t.Fatalf("missing truncation marker")
	}
}

func TestCapCellValue_JSONStringNotDoubleEncoded(t *testing.T) {
	// Explicit raw source must not acquire another layer of JSON quoting.
	got := capCellValue(json.RawMessage(`{"k":"v"}`), true)
	s, ok := got.(json.RawMessage)
	if !ok {
		t.Fatalf("got %T, want json.RawMessage", got)
	}
	if string(s) != `{"k":"v"}` {
		t.Fatalf("got %q, want unchanged", s)
	}
}

func TestReview_json_scalar_codec_retention(t *testing.T) {
	m := pgtype.NewMap()
	for _, oid := range []uint32{pgtype.JSONOID, pgtype.JSONBOID} {
		typ, ok := m.TypeForOID(oid)
		require.True(t, ok)
		for _, format := range []int16{pgtype.TextFormatCode, pgtype.BinaryFormatCode} {
			for _, source := range []string{`""`, `"null"`, `"true"`, `"hello"`, `"{\"a\":1}"`, `"[1]"`, `{"a":1}`, `[1,true,null]`, `true`, `null`} {
				t.Run(fmt.Sprintf("%s/%d/%s", typ.Name, format, source), func(t *testing.T) {
					wire := []byte(source)
					if oid == pgtype.JSONBOID && format == pgtype.BinaryFormatCode {
						wire = append([]byte{1}, wire...)
					}
					decoded, err := typ.Codec.DecodeValue(m, oid, format, wire)
					require.NoError(t, err)
					col := models.ColumnMeta{TypeName: typ.Name, TypeOID: oid}
					vals := []any{decoded}
					capRowValues(vals, []models.ColumnMeta{col})
					if source == "null" {
						require.Nil(t, vals[0]) // Existing pgx nil/SQL NULL contract.
						return
					}
					raw, ok := vals[0].(json.RawMessage)
					require.True(t, ok, "retained JSON must carry explicit provenance")
					var got any
					require.NoError(t, json.Unmarshal(raw, &got))
					require.Equal(t, decoded, got)
					require.Equal(t, string(raw), grid.FormatJSONValue(vals[0]))
					for _, pretty := range []bool{false, true} {
						body := grid.FormatViewerBodyPlain(vals[0], col, pretty)
						require.NoError(t, json.Unmarshal([]byte(body), &got))
						require.Equal(t, decoded, got)
					}
					require.Equal(t, vals[0], capCellValue(vals[0], true))
				})
			}
		}
	}
}

func TestReview_json_scalar_cap_boundary(t *testing.T) {
	for _, n := range []int{MaxStoredCellBytes - 3, MaxStoredCellBytes - 2, MaxStoredCellBytes - 1} {
		s := strings.Repeat("x", n)
		want, err := json.Marshal(s)
		require.NoError(t, err)
		got, ok := capCellValue(s, true).(json.RawMessage)
		require.True(t, ok)
		if len(want) <= MaxStoredCellBytes {
			require.Equal(t, want, []byte(got))
		} else {
			require.Equal(t, string(want[:MaxStoredCellBytes])+cellTruncationMarker, string(got))
		}
	}
	for _, s := range []string{strings.Repeat("界", MaxStoredCellBytes), strings.Repeat("\n", MaxStoredCellBytes)} {
		got, ok := capCellValue(s, true).(json.RawMessage)
		require.True(t, ok)
		require.LessOrEqual(t, len(got), MaxStoredCellBytes+len(cellTruncationMarker))
		require.True(t, utf8.Valid(got))
		require.True(t, strings.HasSuffix(string(got), cellTruncationMarker))
		_, failed := grid.FormatViewerBody(got, models.ColumnMeta{TypeName: "jsonb"}, true)
		require.True(t, failed, "clipped source must retain visible parse failure")
	}
}

func TestColumnIsJSON(t *testing.T) {
	cols := []models.ColumnMeta{
		{TypeName: "jsonb"},
		{TypeName: "JSON"},
		{TypeName: "text"},
		{TypeName: ""},
	}
	want := []bool{true, true, false, false}
	for i, w := range want {
		if got := columnIsJSON(cols, i); got != w {
			t.Fatalf("col %d (%q): got %v, want %v", i, cols[i].TypeName, got, w)
		}
	}
	if columnIsJSON(cols, 99) {
		t.Fatalf("out-of-range index should be false")
	}
}

func TestCapRowValues_InPlace(t *testing.T) {
	cols := []models.ColumnMeta{{TypeName: "text"}, {TypeName: "jsonb"}}
	vals := []any{strings.Repeat("z", MaxStoredCellBytes*2), map[string]any{"a": 1}}
	capRowValues(vals, cols)
	if s := vals[0].(string); len(s) > MaxStoredCellBytes+len(cellTruncationMarker) {
		t.Fatalf("col0 not capped: len=%d", len(s))
	}
	if _, ok := vals[1].(json.RawMessage); !ok {
		t.Fatalf("col1: got %T, want json.RawMessage", vals[1])
	}
}

// equalAny compares two values for the simple cases used in these tests
// (nil, comparable scalars, and byte slices).
func equalAny(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if ab, ok := a.([]byte); ok {
		bb, ok := b.([]byte)
		if !ok || len(ab) != len(bb) {
			return false
		}
		for i := range ab {
			if ab[i] != bb[i] {
				return false
			}
		}
		return true
	}
	return a == b
}
