package grid

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// FormatJSONValue renders a json/jsonb cell value as JSON text. pgx
// decodes objects/arrays into Go maps/slices and string scalars into Go
// strings. Decoded values are marshaled; explicitly raw []byte and
// json.RawMessage sources pass through without another encoding. On a marshal
// error the Go default %v form is returned so a cell still shows something.
//
// Like FormatArrayLiteral, this is the single source of truth for JSON
// cell formatting, shared by the grid renderer and the cell editor's
// edit-seed so display and seed stay identical.
func FormatJSONValue(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(t)
	case json.RawMessage:
		return string(t)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(b)
	}
}

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
// the {a,b,c} form here parses cleanly.
//
// Elements needing it are double-quoted with embedded " and \ escaped;
// a nil element renders as the unquoted NULL keyword. Nested slices
// recurse so multi-dimensional arrays round-trip.
// Valuers supply canonical scalar text. Conversion failure returns the fixed
// non-array message "array value conversion failed" with recognition true,
// preventing callers from falling back to struct dumps or seeding partial data.
func FormatArrayLiteral(v any) (string, bool) {
	s, ok, err := formatArrayLiteral(v)
	if err != nil {
		return "array value conversion failed", true
	}
	return s, ok
}

func formatArrayLiteral(v any) (string, bool, error) {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() || rv.Kind() != reflect.Slice {
		return "", false, nil
	}
	if rv.Type().Elem().Kind() == reflect.Uint8 {
		return "", false, nil // []byte: bytea / raw json, not an array
	}
	var b strings.Builder
	b.WriteByte('{')
	for i := 0; i < rv.Len(); i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		elem, err := formatArrayElem(rv.Index(i).Interface())
		if err != nil {
			return "", true, err
		}
		b.WriteString(elem)
	}
	b.WriteByte('}')
	return b.String(), true, nil
}

// formatArrayElem renders one array element. nil → the bare NULL
// keyword; a nested slice recurses; a Valuer supplies the scalar value.
// Scalars are quoted when they would otherwise break array parsing.
func formatArrayElem(e any) (string, error) {
	if e == nil {
		return "NULL", nil
	}
	if rv := reflect.ValueOf(e); rv.Kind() == reflect.Pointer && rv.IsNil() {
		return "NULL", nil
	}
	if nested, ok, err := formatArrayLiteral(e); ok {
		return nested, err
	}
	if v, ok := e.(driver.Valuer); ok {
		value, err := v.Value()
		if err != nil {
			return "", err
		}
		if value == nil {
			return "NULL", nil
		}
		if !driver.IsValue(value) {
			return "", fmt.Errorf("invalid array driver value: %T", value)
		}
		e = value
		if bytes, ok := value.([]byte); ok {
			e = string(bytes)
		}
	}
	s := fmt.Sprintf("%v", e)
	if arrayElemNeedsQuote(s) {
		r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
		return `"` + r.Replace(s) + `"`, nil
	}
	return s, nil
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
