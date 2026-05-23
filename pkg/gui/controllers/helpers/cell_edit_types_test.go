package helpers_test

import (
	"errors"
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// TestCellEditTypes_CategoryOf walks every TypeName the dispatcher
// classifies and asserts the resulting bucket. Coverage spans the
// in-scope categories (text / int / float / bool / date / timestamp)
// plus the deferred-editor types (json / array / bytea), which must
// route through CategoryDefaultText with a follow-up TODO.
func TestCellEditTypes_CategoryOf(t *testing.T) {
	cases := []struct {
		typeName string
		want     helpers.TypeCategory
	}{
		// Default text.
		{"text", helpers.CategoryDefaultText},
		{"varchar", helpers.CategoryDefaultText},
		{"VARCHAR(64)", helpers.CategoryDefaultText},
		{"char", helpers.CategoryDefaultText},
		{"bpchar", helpers.CategoryDefaultText},
		{"citext", helpers.CategoryDefaultText},
		// Integer numerics.
		{"int2", helpers.CategoryInteger},
		{"int4", helpers.CategoryInteger},
		{"int8", helpers.CategoryInteger},
		{"smallint", helpers.CategoryInteger},
		{"integer", helpers.CategoryInteger},
		{"bigint", helpers.CategoryInteger},
		// Float numerics.
		{"numeric", helpers.CategoryFloat},
		{"decimal", helpers.CategoryFloat},
		{"float4", helpers.CategoryFloat},
		{"float8", helpers.CategoryFloat},
		{"real", helpers.CategoryFloat},
		{"double precision", helpers.CategoryFloat},
		// Boolean.
		{"bool", helpers.CategoryBoolean},
		{"boolean", helpers.CategoryBoolean},
		// Date / timestamp.
		{"date", helpers.CategoryDate},
		{"timestamp", helpers.CategoryTimestamp},
		{"timestamptz", helpers.CategoryTimestamp},
		{"timestamp without time zone", helpers.CategoryTimestamp},
		{"timestamp with time zone", helpers.CategoryTimestamp},
		// Deferred types route through default text.
		{"json", helpers.CategoryDefaultText},
		{"jsonb", helpers.CategoryDefaultText},
		{"bytea", helpers.CategoryDefaultText},
		{"_int4", helpers.CategoryDefaultText},
		// Unknown.
		{"uuid", helpers.CategoryDefaultText},
		{"", helpers.CategoryDefaultText},
	}
	for _, tc := range cases {
		t.Run(tc.typeName, func(t *testing.T) {
			col := models.ColumnMeta{TypeName: tc.typeName}
			if got := helpers.CategoryOf(col); got != tc.want {
				t.Errorf("CategoryOf(%q) = %v, want %v", tc.typeName, got, tc.want)
			}
		})
	}
}

// TestCellEditTypes_NumericRuneFilter asserts the AC: numeric columns
// reject non-digit input at insert time, with `e` permitted only for
// float types. AC quote: "Numeric column rejects non-digit input at
// insert time (other than -, ., e for floats)".
func TestCellEditTypes_NumericRuneFilter(t *testing.T) {
	cases := []struct {
		name     string
		category helpers.TypeCategory
		input    string
		want     string
	}{
		{
			// AC edge-and-negative quote: "Numeric typed input '12abc3'
			// rejects 'a','b','c' (insert ignored), keeps '12','3'".
			name:     "integer strips letters",
			category: helpers.CategoryInteger,
			input:    "12abc3",
			want:     "123",
		},
		{
			name:     "integer rejects decimal point",
			category: helpers.CategoryInteger,
			input:    "12.5",
			want:     "125",
		},
		{
			name:     "integer rejects scientific notation",
			category: helpers.CategoryInteger,
			input:    "1e9",
			want:     "19",
		},
		{
			name:     "integer keeps leading sign",
			category: helpers.CategoryInteger,
			input:    "-42",
			want:     "-42",
		},
		{
			name:     "float accepts decimal point",
			category: helpers.CategoryFloat,
			input:    "3.14",
			want:     "3.14",
		},
		{
			name:     "float accepts scientific notation",
			category: helpers.CategoryFloat,
			input:    "1.5e10",
			want:     "1.5e10",
		},
		{
			name:     "float accepts capital E",
			category: helpers.CategoryFloat,
			input:    "1.5E10",
			want:     "1.5E10",
		},
		{
			name:     "float strips letters except e",
			category: helpers.CategoryFloat,
			input:    "1e2abc",
			want:     "1e2",
		},
		{
			name:     "default text passes input through unchanged",
			category: helpers.CategoryDefaultText,
			input:    "hello world 123",
			want:     "hello world 123",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := helpers.FilterNumericInput(tc.category, tc.input)
			if got != tc.want {
				t.Errorf("FilterNumericInput(%v, %q) = %q, want %q",
					tc.category, tc.input, got, tc.want)
			}
		})
	}
}

// TestCellEditTypes_InitialBuffer_Timestamp asserts the AC: timestamp
// column buffer pre-filled with ISO 8601 of OriginalValue.
func TestCellEditTypes_InitialBuffer_Timestamp(t *testing.T) {
	ts := time.Date(2026, 5, 23, 14, 30, 45, 0, time.UTC)
	col := models.ColumnMeta{TypeName: "timestamptz"}
	got := helpers.InitialBufferFor(col, ts)
	want := "2026-05-23T14:30:45Z"
	if got != want {
		t.Errorf("InitialBufferFor(timestamp) = %q, want %q", got, want)
	}
}

// TestCellEditTypes_InitialBuffer_Date asserts dates pre-fill with
// the calendar-date subset of ISO 8601 (no time portion).
func TestCellEditTypes_InitialBuffer_Date(t *testing.T) {
	ts := time.Date(2026, 5, 23, 14, 30, 45, 0, time.UTC)
	col := models.ColumnMeta{TypeName: "date"}
	got := helpers.InitialBufferFor(col, ts)
	want := "2026-05-23"
	if got != want {
		t.Errorf("InitialBufferFor(date) = %q, want %q", got, want)
	}
}

// TestCellEditTypes_InitialBuffer_NullTimestamp asserts the AC edge
// case: "Timestamp NULL OriginalValue pre-fills empty buffer".
func TestCellEditTypes_InitialBuffer_NullTimestamp(t *testing.T) {
	col := models.ColumnMeta{TypeName: "timestamp"}
	if got := helpers.InitialBufferFor(col, nil); got != "" {
		t.Errorf("InitialBufferFor(nil) = %q, want empty", got)
	}
}

// TestCellEditTypes_InitialBuffer_StringFallback asserts non-timestamp
// types echo a string original verbatim.
func TestCellEditTypes_InitialBuffer_StringFallback(t *testing.T) {
	col := models.ColumnMeta{TypeName: "text"}
	if got := helpers.InitialBufferFor(col, "hello"); got != "hello" {
		t.Errorf("InitialBufferFor(text) = %q, want %q", got, "hello")
	}
}

// TestCellEditTypes_SetNull_Nullable asserts the AC: `<leader>cn` on a
// nullable column stages PendingEdit{Kind:Literal, NewValue=nil}.
func TestCellEditTypes_SetNull_Nullable(t *testing.T) {
	col := models.ColumnMeta{Name: "email", TypeName: "text", Nullable: true}
	pk := []any{int64(7)}
	e, err := helpers.BuildSetNullEdit(pk, col, "old@example.com")
	if err != nil {
		t.Fatalf("BuildSetNullEdit nullable: unexpected err %v", err)
	}
	if e.Kind != models.Literal {
		t.Errorf("Kind = %v, want Literal", e.Kind)
	}
	if e.NewValue != nil {
		t.Errorf("NewValue = %v, want nil", e.NewValue)
	}
	if e.OldValue != "old@example.com" {
		t.Errorf("OldValue = %v, want old@example.com", e.OldValue)
	}
	if e.Column != "email" {
		t.Errorf("Column = %q, want email", e.Column)
	}
	if len(e.PrimaryKey) != 1 || e.PrimaryKey[0] != int64(7) {
		t.Errorf("PrimaryKey = %v, want [7]", e.PrimaryKey)
	}
}

// TestCellEditTypes_SetNull_NotNullable asserts the AMENDMENT: "<leader>cn
// on a column with ColumnMeta.Nullable=false is hard-disabled at edit-
// time with DisabledReason 'column is NOT NULL'".
func TestCellEditTypes_SetNull_NotNullable(t *testing.T) {
	col := models.ColumnMeta{Name: "id", TypeName: "int4", Nullable: false}
	_, err := helpers.BuildSetNullEdit([]any{int64(1)}, col, int64(1))
	if !errors.Is(err, helpers.ErrColumnNotNullable) {
		t.Fatalf("err = %v, want ErrColumnNotNullable", err)
	}
	if err.Error() != "column is NOT NULL" {
		t.Errorf("err.Error() = %q, want %q", err.Error(), "column is NOT NULL")
	}
}

// TestCellEditTypes_BooleanChoice_BuildEdit walks all three chooser
// options and asserts the PendingEdit payload matches the choice.
// ChoiceNull MUST produce NewValue=nil with Kind=Literal, per the AC
// edge-case "Boolean chooser <Esc> with current selection==NULL →
// PendingEdit.NewValue=nil with Kind=Literal".
func TestCellEditTypes_BooleanChoice_BuildEdit(t *testing.T) {
	col := models.ColumnMeta{Name: "active", TypeName: "bool", Nullable: true}
	pk := []any{int64(1)}
	cases := []struct {
		name   string
		choice helpers.BooleanChoice
		want   any
	}{
		{"true", helpers.ChoiceTrue, true},
		{"false", helpers.ChoiceFalse, false},
		{"null", helpers.ChoiceNull, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := helpers.BuildBooleanEdit(tc.choice, pk, col, false)
			if e.Kind != models.Literal {
				t.Errorf("Kind = %v, want Literal", e.Kind)
			}
			if e.NewValue != tc.want {
				t.Errorf("NewValue = %v, want %v", e.NewValue, tc.want)
			}
			if e.Column != "active" {
				t.Errorf("Column = %q, want active", e.Column)
			}
		})
	}
}

// TestCellEditTypes_BooleanChoice_FromKey asserts the key-jump surface:
// t/T → True, f/F → False, n/N → Null. Other keys return ok=false so
// the chooser can ignore them.
func TestCellEditTypes_BooleanChoice_FromKey(t *testing.T) {
	cases := []struct {
		key  rune
		want helpers.BooleanChoice
		ok   bool
	}{
		{'t', helpers.ChoiceTrue, true},
		{'T', helpers.ChoiceTrue, true},
		{'f', helpers.ChoiceFalse, true},
		{'F', helpers.ChoiceFalse, true},
		{'n', helpers.ChoiceNull, true},
		{'N', helpers.ChoiceNull, true},
		{'x', 0, false},
		{'1', 0, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.key), func(t *testing.T) {
			got, ok := helpers.BooleanChoiceFromKey(tc.key)
			if ok != tc.ok {
				t.Errorf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Errorf("choice = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCellEditTypes_InjectNow asserts the AC: "<C-d> injects
// PendingEdit{Kind:Expression, NewExpr:'now()'}".
func TestCellEditTypes_InjectNow(t *testing.T) {
	col := models.ColumnMeta{Name: "updated_at", TypeName: "timestamptz"}
	pk := []any{int64(42)}
	e := helpers.InjectNow(pk, col, time.Time{})
	if e.Kind != models.Expression {
		t.Errorf("Kind = %v, want Expression", e.Kind)
	}
	if e.NewExpr != "now()" {
		t.Errorf("NewExpr = %q, want now()", e.NewExpr)
	}
	if e.Column != "updated_at" {
		t.Errorf("Column = %q, want updated_at", e.Column)
	}
}

// TestCellEditTypes_InjectCurrentDate asserts the AC: "<C-t> injects
// PendingEdit{Kind:Expression, NewExpr:'current_date'}".
func TestCellEditTypes_InjectCurrentDate(t *testing.T) {
	col := models.ColumnMeta{Name: "registered_on", TypeName: "date"}
	pk := []any{int64(99)}
	e := helpers.InjectCurrentDate(pk, col, time.Time{})
	if e.Kind != models.Expression {
		t.Errorf("Kind = %v, want Expression", e.Kind)
	}
	if e.NewExpr != "current_date" {
		t.Errorf("NewExpr = %q, want current_date", e.NewExpr)
	}
}

// TestCellEditTypes_BuildExprEdit asserts the `<leader>ce` PendingEdit
// shape: free-form expression carried verbatim.
func TestCellEditTypes_BuildExprEdit(t *testing.T) {
	col := models.ColumnMeta{Name: "count", TypeName: "int4"}
	pk := []any{int64(3)}
	e := helpers.BuildExprEdit(pk, col, int64(5), "count + 1")
	if e.Kind != models.Expression {
		t.Errorf("Kind = %v, want Expression", e.Kind)
	}
	if e.NewExpr != "count + 1" {
		t.Errorf("NewExpr = %q, want count + 1", e.NewExpr)
	}
	if e.OldValue != int64(5) {
		t.Errorf("OldValue = %v, want 5", e.OldValue)
	}
}

// TestCellEditTypes_WarnPromptLabel asserts the amendment: warning text
// MUST assert "expressions are injected verbatim" (verbatim substring
// match — exact wording may shift but the warning intent must remain).
func TestCellEditTypes_WarnPromptLabel(t *testing.T) {
	if helpers.WarnExprPromptLabel == "" {
		t.Fatal("WarnExprPromptLabel is empty; warning must be present every open")
	}
	if !contains(helpers.WarnExprPromptLabel, "verbatim") {
		t.Errorf("WarnExprPromptLabel = %q, want it to contain 'verbatim'",
			helpers.WarnExprPromptLabel)
	}
}

// TestCellEditTypes_IsDeferredEditor asserts the deferred-editor set
// (json / jsonb / bytea / arrays) is flagged so the controller can
// surface the TODO in a future bd issue rather than silently dropping
// into the default branch.
func TestCellEditTypes_IsDeferredEditor(t *testing.T) {
	cases := []struct {
		typeName string
		want     bool
	}{
		{"json", true},
		{"jsonb", true},
		{"bytea", true},
		{"_int4", true},
		{"_text", true},
		{"text", false},
		{"int4", false},
		{"bool", false},
	}
	for _, tc := range cases {
		t.Run(tc.typeName, func(t *testing.T) {
			col := models.ColumnMeta{TypeName: tc.typeName}
			if got := helpers.IsDeferredEditor(col); got != tc.want {
				t.Errorf("IsDeferredEditor(%q) = %v, want %v", tc.typeName, got, tc.want)
			}
		})
	}
}

// contains is a tiny zero-import substring check for the warning-label
// assertion above. strings.Contains would do but pulling strings just
// for one call is overkill in test code.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
