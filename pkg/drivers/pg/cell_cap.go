package pg

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/davesavic/pgsavvy/pkg/models"
)

// MaxStoredCellBytes bounds the in-memory size of a single decoded cell
// value retained in a buffered result row. Values larger than this are
// truncated at the stream boundary so a wide-payload query (e.g. a jsonb
// audit log) cannot accumulate hundreds of MB of heap across the buffered
// rows and stall the whole process under GC pressure.
//
// It sits far above the 10 KB display cap (grid.MaxCellRenderBytes) and the
// 10 KB yank/export cap, so on-screen output and copy/export are unaffected
// for any realistic value — only pathologically large cells are clipped.
const MaxStoredCellBytes = 64 * 1024

// cellTruncationMarker is appended to a clipped textual value so the user
// can tell the cell was truncated for display rather than ending naturally.
const cellTruncationMarker = "…[truncated]"

// capRowValues clips oversized values in vals in place, using the column
// metadata to decide how each value is rendered (json columns are
// normalised to canonical JSON text so the per-frame renderer no longer
// re-marshals the structured value on every paint).
func capRowValues(vals []any, cols []models.ColumnMeta) {
	for i := range vals {
		vals[i] = capCellValue(vals[i], columnIsJSON(cols, i))
	}
}

// columnIsJSON reports whether column i is a json/jsonb column.
func columnIsJSON(cols []models.ColumnMeta, i int) bool {
	if i < 0 || i >= len(cols) {
		return false
	}
	switch strings.ToLower(cols[i].TypeName) {
	case "json", "jsonb":
		return true
	default:
		return false
	}
}

// capCellValue returns v unchanged when it fits within MaxStoredCellBytes,
// or a clipped form otherwise. nil (SQL NULL) is always preserved so a
// NULL cell never turns into the JSON "null" literal.
func capCellValue(v any, isJSON bool) any {
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case string:
		if len(t) <= MaxStoredCellBytes {
			return v
		}
		return truncateUTF8(t, MaxStoredCellBytes) + cellTruncationMarker
	case []byte:
		if len(t) <= MaxStoredCellBytes {
			return v
		}
		// Binary (bytea) is shown as a hex preview, so no text marker is
		// appended; a rune-safe clip keeps json-as-bytes valid for display.
		return []byte(truncateUTF8(string(t), MaxStoredCellBytes))
	case json.RawMessage:
		if len(t) <= MaxStoredCellBytes {
			return v
		}
		return json.RawMessage(truncateUTF8(string(t), MaxStoredCellBytes) + cellTruncationMarker)
	default:
		if !isJSON {
			return v
		}
		// jsonb object/array/scalar decoded into a Go map/slice/number.
		// Normalise to canonical JSON text once here so the renderer no
		// longer marshals the structured value on every frame, and clip
		// it when oversized.
		b, err := json.Marshal(v)
		if err != nil {
			return v
		}
		if len(b) > MaxStoredCellBytes {
			return json.RawMessage(truncateUTF8(string(b), MaxStoredCellBytes) + cellTruncationMarker)
		}
		return json.RawMessage(b)
	}
}

// truncateUTF8 returns the largest prefix of s whose byte length is at most
// limit and which ends on a UTF-8 rune boundary, so a multibyte rune is
// never split.
func truncateUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
