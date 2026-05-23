package helpers

import (
	"errors"
	"strings"
	"time"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// cell_edit_types.go: per-type entry helpers for the inline cell editor
// (dbsavvy-bwq.5 / A2). Pure factories with no GUI dependencies so the
// CellEditorController can call them from any context. Unit tests drive
// the helpers directly without spinning up the popup machinery.
//
// Sub-popup wiring (boolean chooser as a sub-TEMPORARY_POPUP within
// CELL_EDITOR) and warning-themed PROMPT rendering are owned by the
// controller / orchestrator; this file's helpers return PendingEdit
// values + a TypeCategory so the controller can branch.

// TypeCategory bins a column's TypeName into one of the entry-helper
// dispatch buckets. Unknown / not-yet-implemented types (json, jsonb,
// enum, array, bytea, etc.) fall through to CategoryDefaultText so the
// user can still type a value; server-side validation catches malformed
// input on commit.
type TypeCategory int

const (
	// CategoryDefaultText routes through the plain single-line buffer.
	// Used for text / varchar / char / citext AND for the deferred
	// types (json / jsonb / enum / array / bytea) until dedicated
	// editors land in a follow-up bd issue.
	CategoryDefaultText TypeCategory = iota
	// CategoryInteger restricts insert-time input to digits and a
	// leading sign. Float-only characters (`.`, `e`, `E`) are rejected.
	CategoryInteger
	// CategoryFloat additionally accepts `.` and `e`/`E` (scientific
	// notation) on top of the integer-allowed set.
	CategoryFloat
	// CategoryBoolean dispatches to the three-option chooser overlay
	// (true / false / NULL).
	CategoryBoolean
	// CategoryDate pre-fills the buffer with an ISO 8601 calendar date
	// (YYYY-MM-DD).
	CategoryDate
	// CategoryTimestamp pre-fills the buffer with an ISO 8601 timestamp
	// (with or without tz, depending on column type).
	CategoryTimestamp
)

// CategoryOf classifies col by TypeName. Matching is case-insensitive
// and ignores Postgres' length / precision modifiers (e.g. `varchar(64)`
// → "varchar" → CategoryDefaultText).
//
// Unknown TypeName values fall through to CategoryDefaultText. Caller
// can inspect col.TypeName alongside this category for deferred-editor
// TODO markers (json / jsonb / enum / array / bytea).
func CategoryOf(col models.ColumnMeta) TypeCategory {
	name := normalizeTypeName(col.TypeName)
	switch name {
	case "text", "varchar", "char", "bpchar", "citext", "name":
		return CategoryDefaultText
	case "int2", "int4", "int8", "smallint", "integer", "bigint":
		return CategoryInteger
	case "numeric", "decimal", "float4", "float8", "real",
		"double precision":
		return CategoryFloat
	case "bool", "boolean":
		return CategoryBoolean
	case "date":
		return CategoryDate
	case "timestamp", "timestamptz",
		"timestamp without time zone", "timestamp with time zone":
		return CategoryTimestamp
	default:
		// json / jsonb / enum-typed columns / array types (`_int4` …) /
		// bytea / uuid / interval / inet / cidr / etc. all land here.
		// TODO(dbsavvy follow-up): dedicated editors for json / jsonb /
		// enum / array / bytea per epic Out-of-Scope list.
		return CategoryDefaultText
	}
}

// normalizeTypeName lower-cases s and trims a trailing `(...)` modifier
// so `VARCHAR(64)` → "varchar". Pg's element-array prefix `_` is left
// alone so callers can detect arrays via TypeName even though the
// dispatcher routes them through default text.
func normalizeTypeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.IndexByte(s, '('); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}

// InitialBufferFor returns the string the popup should seed into the
// edit buffer when `i` opens. Mirrors the on-screen rendering so the
// user starts editing what they saw — with one exception: date /
// timestamp columns are normalised to ISO 8601 so `e`/`o` motions land
// on field boundaries the user can predict.
//
// nil values render as "" so backspace doesn't have to skip "NULL".
func InitialBufferFor(col models.ColumnMeta, original any) string {
	if original == nil {
		return ""
	}
	switch CategoryOf(col) {
	case CategoryDate:
		if t, ok := original.(time.Time); ok {
			return t.Format("2006-01-02")
		}
	case CategoryTimestamp:
		if t, ok := original.(time.Time); ok {
			// RFC3339 is a strict subset of ISO 8601 and matches
			// pgx's default time.Time round-trip.
			return t.Format(time.RFC3339)
		}
	}
	if s, ok := original.(string); ok {
		return s
	}
	return ""
}

// IsNumericRuneAccepted reports whether r is a legal insert-time
// character for a numeric column. For CategoryInteger the allowed set
// is digits + '-' + '+'; CategoryFloat additionally accepts '.' and
// 'e'/'E' (scientific notation). All other categories return true
// (insertion is unrestricted; type-specific validation is the editor's
// concern there).
//
// Note: positional rules (e.g. only one leading sign, exactly one
// decimal point) are NOT enforced here — that's the line-buffer's job
// once the rune cleared this filter. Keeping the rule per-rune avoids
// having to re-validate the whole string on every keystroke.
func IsNumericRuneAccepted(category TypeCategory, r rune) bool {
	switch category {
	case CategoryInteger:
		return (r >= '0' && r <= '9') || r == '-' || r == '+'
	case CategoryFloat:
		return (r >= '0' && r <= '9') ||
			r == '-' || r == '+' || r == '.' ||
			r == 'e' || r == 'E'
	default:
		return true
	}
}

// FilterNumericInput returns s with all runes rejected by
// IsNumericRuneAccepted stripped out. Used by tests to simulate the
// editor's per-keystroke filter on a pasted-style string.
func FilterNumericInput(category TypeCategory, s string) string {
	if category != CategoryInteger && category != CategoryFloat {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if IsNumericRuneAccepted(category, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// BooleanChoice enumerates the 3 options surfaced by the boolean
// chooser overlay. The order matches the visible chooser order so a
// tests-as-spec consumer can iterate ChoiceTrue, ChoiceFalse,
// ChoiceNull deterministically.
type BooleanChoice int

const (
	// ChoiceTrue stages NewValue=true with Kind=Literal.
	ChoiceTrue BooleanChoice = iota
	// ChoiceFalse stages NewValue=false with Kind=Literal.
	ChoiceFalse
	// ChoiceNull stages NewValue=nil with Kind=Literal (same payload
	// as SetNull but reachable from the boolean overlay).
	ChoiceNull
)

// BooleanChoiceFromKey returns the chooser option matching the typed
// key. The key surface is t/f/n (case-insensitive). ok=false on any
// other rune.
func BooleanChoiceFromKey(r rune) (BooleanChoice, bool) {
	switch r {
	case 't', 'T':
		return ChoiceTrue, true
	case 'f', 'F':
		return ChoiceFalse, true
	case 'n', 'N':
		return ChoiceNull, true
	default:
		return 0, false
	}
}

// ErrColumnNotNullable is returned by BuildSetNullEdit when the target
// column carries a NOT NULL constraint. The controller surfaces this as
// the `<leader>cn` DisabledReason rather than recording a guaranteed-
// failing edit. Mirrors A1's existing "no active result grid" /
// "read-only connection" disable strings.
var ErrColumnNotNullable = errors.New("column is NOT NULL")

// BuildSetNullEdit returns a literal-kind PendingEdit with NewValue=nil
// for (pk, col). Returns ErrColumnNotNullable if col.Nullable is false
// (caller must surface that as a hard-disable on `<leader>cn`).
//
// Idempotency note: PendingEditSet.Add already replaces an edit for the
// same (pk, col) in place, so calling BuildSetNullEdit twice on the
// same cell does NOT double-record. Tests cover this via the store's
// existing Add semantics; this helper has no internal state.
func BuildSetNullEdit(pk []any, col models.ColumnMeta, old any) (models.PendingEdit, error) {
	if !col.Nullable {
		return models.PendingEdit{}, ErrColumnNotNullable
	}
	return models.PendingEdit{
		PrimaryKey: append([]any(nil), pk...),
		Column:     col.Name,
		OldValue:   old,
		NewValue:   nil,
		Kind:       models.Literal,
		LoadedAt:   time.Now(),
	}, nil
}

// BuildBooleanEdit returns the PendingEdit a boolean-chooser selection
// resolves to. ChoiceNull maps to NewValue=nil (Literal); ChoiceTrue /
// ChoiceFalse map to the corresponding bool literal.
func BuildBooleanEdit(choice BooleanChoice, pk []any, col models.ColumnMeta, old any) models.PendingEdit {
	e := models.PendingEdit{
		PrimaryKey: append([]any(nil), pk...),
		Column:     col.Name,
		OldValue:   old,
		Kind:       models.Literal,
		LoadedAt:   time.Now(),
	}
	switch choice {
	case ChoiceTrue:
		e.NewValue = true
	case ChoiceFalse:
		e.NewValue = false
	case ChoiceNull:
		e.NewValue = nil
	}
	return e
}

// BuildExprEdit returns an Expression-kind PendingEdit with NewExpr=expr.
// expr is taken verbatim — the warning-themed PROMPT label drives that
// home to the user. Caller is responsible for the warning rendering
// (Z1 wires the WarnBorder theme color; until then the controller uses
// a placeholder label).
//
// Empty expr is still returned as-is; the controller decides whether to
// elide on empty input.
func BuildExprEdit(pk []any, col models.ColumnMeta, old any, expr string) models.PendingEdit {
	return models.PendingEdit{
		PrimaryKey: append([]any(nil), pk...),
		Column:     col.Name,
		OldValue:   old,
		NewExpr:    expr,
		Kind:       models.Expression,
		LoadedAt:   time.Now(),
	}
}

// InjectNow returns the canonical `<C-d>` PendingEdit: Expression-kind
// with NewExpr="now()". Per AC dbsavvy-bwq.5 ("<C-d> injects
// PendingEdit{Kind:Expression, NewExpr:'now()'}").
func InjectNow(pk []any, col models.ColumnMeta, old any) models.PendingEdit {
	return BuildExprEdit(pk, col, old, "now()")
}

// InjectCurrentDate returns the canonical `<C-t>` PendingEdit:
// Expression-kind with NewExpr="current_date". Per AC dbsavvy-bwq.5
// ("<C-t> injects PendingEdit{Kind:Expression, NewExpr:'current_date'}").
func InjectCurrentDate(pk []any, col models.ColumnMeta, old any) models.PendingEdit {
	return BuildExprEdit(pk, col, old, "current_date")
}

// WarnExprPromptLabel is the user-facing label the `<leader>ce` prompt
// renders. Per amendment (review-plan), the warning text MUST assert
// "expressions are injected verbatim" so a confused user can't claim
// they were not warned. The warning is rendered every open — no
// suppression flag.
//
// TODO(dbsavvy-bwq.23 / Z1): the prompt's border colour switches to
// WarnBorder (new theme key in Z1) once that theme entry lands; this
// helper supplies only the text.
const WarnExprPromptLabel = "expr (injected verbatim, no escaping): "

// IsDeferredEditor reports whether col.TypeName falls into the
// deferred-editor set (json / jsonb / enum / array / bytea). The
// controller routes these through the default text buffer with a TODO
// pointing at a follow-up bd issue; this helper exists so that TODO is
// surfaced in tests as a known-deferred behaviour rather than silently
// dropping into the default branch.
//
// TODO(dbsavvy follow-up bd): dedicated editors for these types.
func IsDeferredEditor(col models.ColumnMeta) bool {
	name := normalizeTypeName(col.TypeName)
	switch name {
	case "json", "jsonb", "bytea":
		return true
	}
	// Array element types in pgx are reported with a leading underscore
	// (e.g. `_int4`). Treat any such prefix as a deferred array editor.
	if strings.HasPrefix(name, "_") {
		return true
	}
	// Enum types come through with the enum's TypeName (no canonical
	// prefix). Detecting them requires pg_type lookup which is out of
	// scope for this helper; the controller falls back to default text
	// and the user gets a generic editor with server-side validation.
	return false
}
