package grid

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFormatArrayLiteral_SimpleStrings is the regression for dbsavvy-26i:
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
