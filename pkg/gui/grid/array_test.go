package grid

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"math/big"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/pgsavvy/pkg/models"
)

type reviewArrayValuer struct {
	value driver.Value
	err   error
}

func (v reviewArrayValuer) Value() (driver.Value, error) { return v.value, v.err }

func TestReview_numeric_array(t *testing.T) {
	col := models.ColumnMeta{TypeName: "_numeric"}
	t.Run("actual_codec", func(t *testing.T) {
		m := pgtype.NewMap()
		arrayType, ok := m.TypeForOID(pgtype.NumericArrayOID)
		require.True(t, ok)
		literal := "{123456789012345678901234567890.12345678901234567890,-0.00000000000000000001,0,NULL,NaN,Infinity,-Infinity}"
		for _, format := range []int16{pgtype.TextFormatCode, pgtype.BinaryFormatCode} {
			src := []byte(literal)
			if format == pgtype.BinaryFormatCode {
				var values []pgtype.Numeric
				require.NoError(t, m.Scan(pgtype.NumericArrayOID, pgtype.TextFormatCode, src, &values))
				var err error
				src, err = m.Encode(pgtype.NumericArrayOID, format, values, nil)
				require.NoError(t, err)
			}
			decoded, err := arrayType.Codec.DecodeValue(m, pgtype.NumericArrayOID, format, src)
			require.NoError(t, err)
			require.IsType(t, pgtype.Numeric{}, decoded.([]any)[0])
			got, recognized := FormatArrayLiteral(decoded)
			require.True(t, recognized)
			require.Equal(t, literal, got)
			require.Equal(t, literal, renderCellPlain(decoded, col))
			var reparsed []pgtype.Numeric
			require.NoError(t, m.Scan(pgtype.NumericArrayOID, pgtype.TextFormatCode, []byte(got), &reparsed))
			require.Len(t, reparsed, 7)
			require.False(t, reparsed[3].Valid)
			for i, value := range decoded.([]any) {
				if value == nil {
					continue
				}
				want, err := value.(pgtype.Numeric).Value()
				require.NoError(t, err)
				actual, err := reparsed[i].Value()
				require.NoError(t, err)
				require.Equal(t, want, actual)
			}
		}
	})

	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"positive_exponent", []pgtype.Numeric{{Int: big.NewInt(123), Exp: 8, Valid: true}}, "{12300000000}"},
		{"negative_exponent", []pgtype.Numeric{{Int: big.NewInt(-123), Exp: -8, Valid: true}}, "{-0.00000123}"},
		{"typed_null", []pgtype.Numeric{{}}, "{NULL}"},
		{"nil_numeric_pointer", []*pgtype.Numeric{nil}, "{NULL}"},
		{"nested_and_escaped", []any{[]any{pgtype.Numeric{Int: big.NewInt(123), Valid: true}, pgtype.Numeric{}}, []any{`quote"\,`, "NULL"}}, `{{123,NULL},{"quote\"\\,","NULL"}}`},
		{"valuer_nil", []any{reviewArrayValuer{}}, "{NULL}"},
		{"valuer_escaping", []any{reviewArrayValuer{value: `quote"\,`}}, `{"quote\"\\,"}`},
		{"conversion_error", []any{1, reviewArrayValuer{value: "123", err: errors.New("synthetic conversion detail")}, 2}, "array value conversion failed"},
		{"nested_conversion_error", []any{[]any{1}, []any{reviewArrayValuer{err: errors.New("synthetic conversion detail")}}}, "array value conversion failed"},
		{"invalid_driver_value", []any{reviewArrayValuer{value: struct{}{}}}, "array value conversion failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := FormatArrayLiteral(tc.value)
			require.True(t, ok)
			require.Equal(t, tc.want, got)
			require.Equal(t, tc.want, renderCellPlain(tc.value, col))
		})
	}
}

func TestReview_json_scalar_display(t *testing.T) {
	col := models.ColumnMeta{TypeName: "jsonb"}
	for _, s := range []string{"", "null", "true", "hello", `{"a":1}`, `[1]`, "quote\"\\\n界"} {
		t.Run(s, func(t *testing.T) {
			want, err := json.Marshal(s)
			require.NoError(t, err)
			require.Equal(t, string(want), FormatJSONValue(s))
			require.Equal(t, string(want), renderCellPlain(s, col))
		})
	}
	for _, raw := range []string{`{"a":1}`, `[1,true,null]`, `"true"`, `null`, `true`} {
		require.Equal(t, raw, FormatJSONValue(json.RawMessage(raw)))
		require.Equal(t, raw, FormatJSONValue([]byte(raw)))
	}
	require.Equal(t, "", FormatJSONValue(nil))
	require.Equal(t, "NULL", renderCellPlain(nil, col))
	require.Equal(t, "hello", renderCellPlain("hello", models.ColumnMeta{TypeName: "text"}))
}

// TestFormatArrayLiteral_SimpleStrings is the regression test:
// pgx decodes a text[] column into a Go slice; rendering it with fmt's
// "%v" yields "[admin founder editor]" (square brackets, space-joined),
// which Postgres rejects as a "malformed array literal". The user sees
// and edits that string, then the commit fails. FormatArrayLiteral must
// instead emit Postgres array *input* syntax: {admin,founder,editor}.
func TestFormatArrayLiteral_SimpleStrings(t *testing.T) {
	got, ok := FormatArrayLiteral([]any{"admin", "founder", "editor"})
	require.True(t, ok, "a []any must be recognised as array-shaped")
	require.Equal(t, "{admin,founder,editor}", got)
}

func TestFormatArrayLiteral_TypedStringSlice(t *testing.T) {
	got, ok := FormatArrayLiteral([]string{"a", "b"})
	require.True(t, ok)
	require.Equal(t, "{a,b}", got)
}

func TestFormatArrayLiteral_IntSlice(t *testing.T) {
	got, ok := FormatArrayLiteral([]int32{1, 2, 3})
	require.True(t, ok)
	require.Equal(t, "{1,2,3}", got)
}

// Elements containing the array metacharacters (comma, braces, quotes,
// backslash, whitespace) or that look like the NULL keyword must be
// double-quoted, with embedded " and \ backslash-escaped, so Postgres
// parses them as a single literal element rather than mis-splitting.
func TestFormatArrayLiteral_QuotesSpecialElements(t *testing.T) {
	got, ok := FormatArrayLiteral([]any{"a,b", "c d", `q"x`, `back\slash`, "NULL", ""})
	require.True(t, ok)
	require.Equal(t, `{"a,b","c d","q\"x","back\\slash","NULL",""}`, got)
}

// A genuine SQL NULL element (nil) renders as the unquoted NULL keyword,
// distinct from the literal string "NULL" which gets quoted above.
func TestFormatArrayLiteral_NilElementIsKeyword(t *testing.T) {
	got, ok := FormatArrayLiteral([]any{"x", nil, "y"})
	require.True(t, ok)
	require.Equal(t, "{x,NULL,y}", got)
}

func TestFormatArrayLiteral_Empty(t *testing.T) {
	got, ok := FormatArrayLiteral([]any{})
	require.True(t, ok)
	require.Equal(t, "{}", got)
}

// Nested slices recurse so multi-dimensional arrays round-trip into the
// {{..},{..}} form Postgres expects.
func TestFormatArrayLiteral_Nested(t *testing.T) {
	got, ok := FormatArrayLiteral([]any{[]any{"a", "b"}, []any{"c"}})
	require.True(t, ok)
	require.Equal(t, "{{a,b},{c}}", got)
}

// Non-array values must report ok=false so callers fall back to their
// existing scalar formatting. []byte is the critical case: bytea and raw
// json decode to []byte and must NOT be treated as arrays.
func TestFormatArrayLiteral_RejectsNonArrays(t *testing.T) {
	cases := []any{
		nil,
		"plain string",
		int64(42),
		[]byte("not an array"),
	}
	for _, v := range cases {
		_, ok := FormatArrayLiteral(v)
		require.Falsef(t, ok, "%T (%v) must not be treated as an array", v, v)
	}
}
