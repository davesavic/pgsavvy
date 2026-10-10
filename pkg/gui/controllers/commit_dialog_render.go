package controllers

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers"
	"github.com/davesavic/pgsavvy/pkg/gui/editor/highlight"
	"github.com/davesavic/pgsavvy/pkg/gui/grid"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/theme"
)

// Render budgets for the 80×24 terminal target. Tight numbers mirror
// the minimum-viable terminal size; oversize terminals get the same
// truncation (the dialog is intentionally compact).
const (
	// commitDialogMaxRows caps the number of row-diff rows the Preview
	// body renders before collapsing into a `... (N more rows)` footer.
	commitDialogMaxRows = 12
	// commitDialogMaxStmts caps the number of SQL statements rendered
	// in SqlPreview / DryRunResult bodies before the same truncation.
	commitDialogMaxStmts = 12
	// commitDialogValueWidth caps a single column value's rendered
	// width before it's truncated with an ellipsis. Keeps wide diffs
	// from blowing the 80-col budget.
	commitDialogValueWidth = 32
)

// commitDialogFooterSqlNote is the always-on footer reminding the user
// that Expression edits are evaluated server-side at COMMIT time, not
// resolved to literal values in the client. Required by the epic
// amendment.
const commitDialogFooterSqlNote = "expressions execute server-side at COMMIT"

// DefaultCommitDialogRender is the renderHook installed by the
// controller. Exposed (not lowercase) so unit tests can call it
// directly with a synthetic CommitDialogView.
func DefaultCommitDialogRender(v guicontext.CommitDialogView) string {
	v = redactedCommitView(v)
	var b strings.Builder
	writeCommitDialogHeader(&b, v)
	b.WriteByte('\n')

	switch v.Mode {
	case guicontext.CommitDialogSqlPreview:
		writeCommitDialogSQL(&b, v)
	case guicontext.CommitDialogDryRunResult:
		writeCommitDialogDryRun(&b, v)
	default:
		writeCommitDialogPreview(&b, v)
	}

	b.WriteByte('\n')
	writeCommitDialogGate(&b, v)
	b.WriteByte('\n')
	b.WriteString(commitDialogFooterSqlNote)
	return commitTextRedactor(v)(b.String())
}

func commitTextRedactor(v guicontext.CommitDialogView) func(string) string {
	return func(text string) string {
		if v.RedactSecrets != nil {
			text = v.RedactSecrets(text)
		}
		if password := connectionPassword(v.Conn); password != "" {
			encoded := strings.NewReplacer("'", "''", "\\", "\\\\").Replace(password)
			text = strings.ReplaceAll(text, encoded, "***")
			text = strings.ReplaceAll(text, strings.ReplaceAll(password, "'", "''"), "***")
			text = strings.ReplaceAll(text, password, "***")
		}
		return text
	}
}

func redactedCommitView(v guicontext.CommitDialogView) guicontext.CommitDialogView {
	redact := commitTextRedactor(v)
	if v.Set != nil {
		v.PreviewEdits = v.Set.Edits()
		for i, edit := range v.PreviewEdits {
			edit.OldValue = redactPreviewValue(edit.OldValue, redact)
			edit.NewValue = redactPreviewEditValue(edit.NewValue, edit, redact)
			edit.NewExpr = redact(edit.NewExpr)
			v.PreviewEdits[i] = edit
		}
	}
	v.DryRunResult = append([]guicontext.DryRunStmtResult(nil), v.DryRunResult...)
	for i := range v.DryRunResult {
		v.DryRunResult[i].SQL = redact(v.DryRunResult[i].SQL)
	}
	return v
}

func redactPreviewEditValue(value any, edit models.PendingEdit, redact func(string) string) any {
	if source, ok := value.(string); ok && grid.IsJSONColumn(models.ColumnMeta{TypeName: edit.ColumnType, TypeOID: edit.TypeOID}) && json.Valid([]byte(source)) {
		return redactPreviewValue(json.RawMessage(source), redact)
	}
	return redactPreviewValue(value, redact)
}

func redactPreviewValue(value any, redact func(string) string) any {
	switch source := value.(type) {
	case string:
		return redact(source)
	case json.RawMessage:
		var decoded any
		decoder := json.NewDecoder(strings.NewReader(string(source)))
		decoder.UseNumber()
		if decoder.Decode(&decoded) == nil {
			if encoded, err := json.Marshal(redactPreviewValue(decoded, redact)); err == nil {
				return json.RawMessage(encoded)
			}
		}
		return json.RawMessage(redact(string(source)))
	case []byte:
		if json.Valid(source) {
			return []byte(redactPreviewValue(json.RawMessage(source), redact).(json.RawMessage))
		}
		return []byte(redact(string(source)))
	case map[string]any:
		out := make(map[string]any, len(source))
		for key, item := range source {
			out[redact(key)] = redactPreviewValue(item, redact)
		}
		return out
	case []any:
		out := make([]any, len(source))
		for i, item := range source {
			out[i] = redactPreviewValue(item, redact)
		}
		return out
	default:
		rv := reflect.ValueOf(value)
		if rv.IsValid() && (rv.Kind() == reflect.Array || rv.Kind() == reflect.Slice) {
			out := reflect.New(rv.Type()).Elem()
			if rv.Kind() == reflect.Slice {
				out = reflect.MakeSlice(rv.Type(), rv.Len(), rv.Len())
			}
			for i := range rv.Len() {
				item := redactPreviewValue(rv.Index(i).Interface(), redact)
				if item != nil {
					out.Index(i).Set(reflect.ValueOf(item))
				}
			}
			return out.Interface()
		}
		return value
	}
}

// writeCommitDialogHeader emits `<icon> <label> · Commit N changes to
// <schema>.<table>`. Falls back gracefully when icon / label are empty
// (some profiles only carry the bare Name).
func writeCommitDialogHeader(b *strings.Builder, v guicontext.CommitDialogView) {
	icon, label := "", ""
	if v.Conn != nil {
		icon = v.Conn.Icon
		label = v.Conn.Label
		if label == "" {
			label = v.Conn.Name
		}
	}
	if icon != "" {
		b.WriteString(icon)
		b.WriteByte(' ')
	}
	if label != "" {
		b.WriteString(label)
		b.WriteString(" · ")
	}
	n := 0
	tableName := "<table>"
	if v.Set != nil {
		n = v.Set.Count()
		tableName = formatTableRef(v.Set.Table)
	}
	fmt.Fprintf(b, "Commit %d %s to %s", n, pluralChanges(n), tableName)
}

// pluralChanges returns "change" / "changes" matching n. Trivial
// helper, but pulled out so the header reads cleanly.
func pluralChanges(n int) string {
	if n == 1 {
		return "change"
	}
	return "changes"
}

// formatTableRef returns "schema.table" or just "table" when schema is
// empty. Quoting is NOT applied here — the header is human text, not
// SQL; the SQL preview path uses its own quoter.
func formatTableRef(r models.Ref) string {
	if r.Schema == "" {
		return r.Table
	}
	return r.Schema + "." + r.Table
}

// writeCommitDialogPreview renders the per-row diff body. Edits are
// grouped by primary key so each row block lists all column changes
// for that row.
func writeCommitDialogPreview(b *strings.Builder, v guicontext.CommitDialogView) {
	if v.Set == nil || v.Set.IsEmpty() {
		b.WriteString("(no staged edits)")
		return
	}
	groups := groupEditsByPK(v.PreviewEdits)
	total := len(groups)
	shown := min(total, commitDialogMaxRows)
	for i := range shown {
		g := groups[i]
		fmt.Fprintf(b, "row %s:\n", formatPK(redactPreviewValue(g.pk, commitTextRedactor(v)).([]any)))
		for _, e := range g.edits {
			fmt.Fprintf(b, "  %s: %s → %s\n",
				e.Column,
				truncateValue(formatEditValue(e.OldValue, e.ColumnType)),
				formatNew(e),
			)
		}
	}
	if total > shown {
		fmt.Fprintf(b, "... (%d more rows)\n", total-shown)
	}
}

// editGroup pairs a primary-key tuple with the column-level edits
// touching that row.
type editGroup struct {
	pk    []any
	edits []models.PendingEdit
}

// groupEditsByPK collapses a flat []PendingEdit into row-keyed groups,
// preserving first-seen order so the Preview body is stable for tests.
func groupEditsByPK(edits []models.PendingEdit) []editGroup {
	groups := make([]editGroup, 0)
	for _, e := range edits {
		found := -1
		for i := range groups {
			if models.RowIdentityEqual(groups[i].pk, e.PrimaryKey) {
				found = i
				break
			}
		}
		if found >= 0 {
			groups[found].edits = append(groups[found].edits, e)
			continue
		}
		groups = append(groups, editGroup{
			pk:    append([]any(nil), e.PrimaryKey...),
			edits: []models.PendingEdit{e},
		})
	}
	// Stable column order within each group (alphabetical) so SQL
	// preview / preview / dry-run all line up regardless of insertion
	// order.
	for i := range groups {
		sort.SliceStable(groups[i].edits, func(a, b int) bool {
			return groups[i].edits[a].Column < groups[i].edits[b].Column
		})
	}
	return groups
}

// formatPK renders a PK tuple as `(v1, v2, ...)`. Single-column PKs
// drop the parens to keep the row header readable.
func formatPK(pk []any) string {
	if len(pk) == 1 {
		return formatPKValue(pk[0])
	}
	parts := make([]string, len(pk))
	for i, v := range pk {
		parts[i] = formatPKValue(v)
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

func formatPKValue(v any) string {
	if v != nil {
		kind := reflect.TypeOf(v).Kind()
		if kind == reflect.Slice || kind == reflect.Array {
			return fmt.Sprintf("%#v", v)
		}
	}
	return formatValue(v)
}

// formatValue renders an arbitrary Go value in dialog-text form. NULL
// renders as the literal word "NULL"; strings are quoted; everything
// else is %v.
func formatValue(v any) string {
	if v == nil {
		return "NULL"
	}
	if s, ok := v.(string); ok {
		return fmt.Sprintf("%q", s)
	}
	return fmt.Sprintf("%v", v)
}

// formatEditValue renders a staged-edit value (old or new side) for the
// row-diff preview, type-aware. json/jsonb columns render as JSON text —
// the same form the grid and cell editor show — so both sides of the
// arrow read as JSON rather than Go's byte-slice / map form for the old
// value and an escaped-string for the new. NULL still
// renders as the literal word; everything else falls back to formatValue.
func formatEditValue(v any, columnType string) string {
	if v == nil {
		return "NULL"
	}
	if grid.IsJSONColumn(models.ColumnMeta{TypeName: columnType}) {
		return grid.FormatJSONValue(v)
	}
	return formatValue(v)
}

// formatNew renders the right-hand side of a row-diff line: the new
// value for Literal edits, the verbatim expression text (with a
// "(SQL expression)" suffix) for Expression edits. Per the epic
// amendment, the expression text is NOT truncated here so the user
// can see the full source.
func formatNew(e models.PendingEdit) string {
	if e.Kind == models.Expression {
		return e.NewExpr + " (SQL expression)"
	}
	value := e.NewValue
	if grid.IsJSONColumn(models.ColumnMeta{TypeName: e.ColumnType}) {
		if source, ok := value.(string); ok {
			// Staged JSON strings are validated document source, not decoded scalars.
			value = json.RawMessage(source)
		}
	}
	return truncateValue(formatEditValue(value, e.ColumnType))
}

// truncateValue caps a rendered cell value at commitDialogValueWidth.
// Overflow is replaced with a single-char ellipsis so the value still
// reads as truncated rather than "value cut off without warning".
func truncateValue(s string) string {
	if len(s) <= commitDialogValueWidth {
		return s
	}
	return s[:commitDialogValueWidth-1] + "…"
}

// writeCommitDialogSQL renders the BEGIN/COMMIT-wrapped UPDATE
// statements using the unified builder in Literal mode — real PK column
// names from v.PkCols, actual values inlined (not $N placeholders).
// Password scrubbing is applied to values before embedding.
func writeCommitDialogSQL(b *strings.Builder, v guicontext.CommitDialogView) {
	if v.Set == nil || v.Set.IsEmpty() {
		b.WriteString("(no staged edits)")
		return
	}
	edits := v.PreviewEdits
	b.WriteString("BEGIN;\n")
	pkCols := v.PkCols
	if id := v.Set.Identity(); id.RelationOID != 0 {
		pkCols = id.Names()
	}
	if len(pkCols) == 0 {
		pkCols = pkColumnPlaceholder(len(edits[0].PrimaryKey))
	}
	batch, err := BuildCommitPreviewBatch(v.Set.Table, pkCols, edits, commitTextRedactor(v))
	if err != nil {
		b.WriteString("(cannot build safe SQL preview)")
		return
	}
	shown := min(len(batch), commitDialogMaxStmts)
	for i := range shown {
		b.WriteString(highlight.Highlight(commitTextRedactor(v)(batch[i].SQL)))
		b.WriteByte('\n')
	}
	if len(batch) > shown {
		fmt.Fprintf(b, "... (%d more statements)\n", len(batch)-shown)
	}
	b.WriteString("COMMIT;")
}

// BuildCommitPreviewBatch produces display-only SQL. Original identity groups
// and edit indexes survive redaction; callers must never execute this SQL.
func BuildCommitPreviewBatch(table models.Ref, pkCols []string, edits []models.PendingEdit, redact func(string) string) ([]helpers.RowUpdateStatement, error) {
	if redact == nil {
		redact = func(text string) string { return text }
	}
	var batch []helpers.RowUpdateStatement
	for _, group := range groupEditsByPK(edits) {
		indices := make([]int, len(group.edits))
		for i, edit := range group.edits {
			for j, original := range edits {
				if edit.Column == original.Column && models.RowIdentityEqual(edit.PrimaryKey, original.PrimaryKey) {
					indices[i] = j
					break
				}
			}
			edit.PrimaryKey = redactPreviewValue(edit.PrimaryKey, redact).([]any)
			edit.OldValue = redactPreviewValue(edit.OldValue, redact)
			edit.NewValue = redactPreviewEditValue(edit.NewValue, edit, redact)
			edit.NewExpr = redact(edit.NewExpr)
			group.edits[i] = edit
		}
		statements, err := helpers.BuildUpdateBatch(table, pkCols, group.edits, helpers.SQLModeLiteral, true)
		if err != nil {
			return nil, err
		}
		for i := range statements {
			for j, index := range statements[i].EditIndexes {
				statements[i].EditIndexes[j] = indices[index]
			}
		}
		batch = append(batch, statements...)
	}
	return batch, nil
}

// pkColumnPlaceholder returns per-PK-column identifier names for use by
// the SQL preview when real PK column names are not available.
func pkColumnPlaceholder(n int) []string {
	names := make([]string, n)
	for i := range n {
		names[i] = fmt.Sprintf("pk%d", i+1)
	}
	return names
}

// writeCommitDialogDryRun renders the per-statement rows-affected
// report. nil result → instruction to press [d]; empty result → "no
// statements"; otherwise one line per entry.
func writeCommitDialogDryRun(b *strings.Builder, v guicontext.CommitDialogView) {
	if v.DryRunResult == nil {
		b.WriteString("press [d] to run dry-run (BEGIN; ...; ROLLBACK)")
		return
	}
	if len(v.DryRunResult) == 0 {
		b.WriteString("dry-run produced no statements")
		return
	}
	total := len(v.DryRunResult)
	shown := min(total, commitDialogMaxStmts)
	for i := range shown {
		r := v.DryRunResult[i]
		sql := highlight.Highlight(strings.ReplaceAll(r.SQL, "\n", " "))
		switch {
		case r.Err != nil:
			fmt.Fprintf(b, "[ERR] %s: %v\n", sql, r.Err)
		case r.RowsAffected < 0:
			fmt.Fprintf(b, "[? rows] %s\n", sql)
		default:
			fmt.Fprintf(b, "[%d rows] %s\n", r.RowsAffected, sql)
		}
	}
	if total > shown {
		fmt.Fprintf(b, "... (%d more statements)\n", total-shown)
	}
}

// --- Key mnemonic color helpers --------------------------------------------
// ANSI color codes for commit dialog gate mnemonics. When theme.IsMonochrome()
// returns true, fall back to bold-only (no color codes emitted), matching
// the confirmHint() pattern in pkg/gui/context/confirmation_context.go.

const (
	ansiGreen   = "\x1b[32m"
	ansiYellow  = "\x1b[33m"
	ansiCyan    = "\x1b[36m"
	ansiMagenta = "\x1b[35m"
	ansiRed     = "\x1b[31m"
	ansiBold    = "\x1b[1m"
	ansiReset   = "\x1b[0m"
)

// coloredKey wraps a key mnemonic in the given ANSI code. On monochrome
// terminals the color is replaced with bold-only.
func coloredKey(key, ansi string) string {
	if theme.IsMonochrome() {
		return ansiBold + key + ansiReset
	}
	return ansi + ansiBold + key + ansiReset + ansiReset
}

// keyGreen wraps [a] in green.
func keyGreen(key string) string { return coloredKey(key, ansiGreen) }

// keyYellow wraps [d] in yellow.
func keyYellow(key string) string { return coloredKey(key, ansiYellow) }

// keyCyan wraps [s] in cyan.
func keyCyan(key string) string { return coloredKey(key, ansiCyan) }

// keyMagenta wraps [t] in magenta.
func keyMagenta(key string) string { return coloredKey(key, ansiMagenta) }

// keyRed wraps [Esc] / [c] in red.
func keyRed(key string) string { return coloredKey(key, ansiRed) }

// coloredLine wraps a full line in green (used for the confirmation-success
// banner). On monochrome terminals falls back to bold.
func coloredLine(line string) string {
	if theme.IsMonochrome() {
		return ansiBold + line + ansiReset
	}
	return ansiGreen + ansiBold + line + ansiReset + ansiReset
}

// --- Gate rendering --------------------------------------------------------

// writeCommitDialogGate renders the apply-enable hint. Behavior depends on
// connection profile:
//
//   - confirm_writes, before typed-name match: [t] type "name" to confirm:
//     <typed>  [Esc] cancel  (no [a]/[d]/[s]).
//   - confirm_writes, after match: ✓ name confirmed  [a] apply  [d] dry-run
//     [s] sql  [Esc] cancel.
//   - ReadOnly: [s] sql  [Esc] cancel  ([a]/[d] hidden).
//   - Empty connection name + confirm_writes: gate permanently disabled
//     (no name to match).
//   - Default: [a] apply  [d] dry-run  [s] sql  [Esc] cancel.
func writeCommitDialogGate(b *strings.Builder, v guicontext.CommitDialogView) {
	if v.Conn != nil && v.Conn.ConfirmWrites {
		if v.Conn.Name == "" {
			fmt.Fprintf(b, "%s empty connection name — gate disabled\n", keyMagenta("[t]"))
			b.WriteString(keyRed("[Esc]") + " cancel")
			return
		}
		if v.TypedName != v.Conn.Name {
			fmt.Fprintf(b, "%s type %q to confirm: %s\n", keyMagenta("[t]"), v.Conn.Name, v.TypedName)
			b.WriteString(keyRed("[Esc]") + " cancel")
			return
		}
		fmt.Fprint(b, coloredLine(fmt.Sprintf("✓ %s confirmed", v.Conn.Name)))
		b.WriteString("\n")
	}
	if v.Conn != nil && v.Conn.ReadOnly {
		fmt.Fprintf(b, "%s sql  %s cancel", keyCyan("[s]"), keyRed("[Esc]"))
		return
	}
	fmt.Fprintf(b, "%s apply  %s dry-run  %s sql  %s cancel",
		keyGreen("[a]"), keyYellow("[d]"), keyCyan("[s]"), keyRed("[Esc]"))
}

// connectionPassword returns the plaintext password on the connection
// profile when ConfirmWrites callers have asked the SQL preview to
// scrub it. Empty when the profile uses keyring / pgpass / exec.
func connectionPassword(c *models.Connection) string {
	if c == nil {
		return ""
	}
	return c.Password
}
