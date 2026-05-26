package grid

import (
	"fmt"
	"reflect"
	"strings"
)

// FormatArrayLiteral renders a Go slice — the shape pgx decodes a
// Postgres array column into — as Postgres array *input* syntax:
// {elem,elem,...}. It returns ("", false) when v is not array-shaped
// (nil, a scalar, or a []byte: bytea and raw json decode to []byte and
// must not be treated as arrays), letting callers fall back to their
// scalar formatting.
//
// This is the single source of truth for turning a decoded array value
// back into a string — used both by the grid cell renderer and by the
// cell editor's edit-seed (pkg/gui/orchestrator/cell_editor_adapters.go)
// so display and seed stay identical. It matters because the commit path
// submits the edited string verbatim: Go's default "[a b c]" slice
// formatting is rejected by Postgres ("malformed array literal"), whereas
// the {a,b,c} form here parses cleanly (dbsavvy-26i).
//
// Elements needing it are double-quoted with embedded " and \ escaped;
// a nil element renders as the unquoted NULL keyword. Nested slices
// recurse so multi-dimensional arrays round-trip.
func FormatArrayLiteral(v any) (string, bool) {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() || rv.Kind() != reflect.Slice {
		return "", false
	}
	if rv.Type().Elem().Kind() == reflect.Uint8 {
		return "", false // []byte: bytea / raw json, not an array
	}
	var b strings.Builder
	b.WriteByte('{')
	for i := 0; i < rv.Len(); i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(formatArrayElem(rv.Index(i).Interface()))
	}
	b.WriteByte('}')
	return b.String(), true
}

// formatArrayElem renders one array element. nil → the bare NULL
// keyword; a nested slice recurses; everything else is stringified via
// %v and quoted when it would otherwise break array parsing.
func formatArrayElem(e any) string {
	if e == nil {
		return "NULL"
	}
	if nested, ok := FormatArrayLiteral(e); ok {
		return nested
	}
	s := fmt.Sprintf("%v", e)
	if arrayElemNeedsQuote(s) {
		r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
		return `"` + r.Replace(s) + `"`
	}
	return s
}

// arrayElemNeedsQuote reports whether an element's string form must be
// double-quoted in a Postgres array literal: empty strings, the literal
// "NULL" keyword (so it isn't read as SQL NULL), and any element bearing
// the array metacharacters or whitespace.
func arrayElemNeedsQuote(s string) bool {
	if s == "" || strings.EqualFold(s, "NULL") {
		return true
	}
	return strings.ContainsAny(s, " \t\n\r,{}\"\\")
}
