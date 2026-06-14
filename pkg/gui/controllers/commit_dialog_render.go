package controllers

import (
	"fmt"
	"sort"
	"strings"

	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/editor/highlight"
	"github.com/davesavic/dbsavvy/pkg/gui/grid"
	"github.com/davesavic/dbsavvy/pkg/models"
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
	return b.String()
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
	groups := groupEditsByPK(v.Set.Edits())
	total := len(groups)
	shown := min(total, commitDialogMaxRows)
	for i := range shown {
		g := groups[i]
		fmt.Fprintf(b, "row %s:\n", formatPK(g.pk))
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
	idx := map[string]int{}
	for _, e := range edits {
		key := pkKey(e.PrimaryKey)
		if i, ok := idx[key]; ok {
			groups[i].edits = append(groups[i].edits, e)
			continue
		}
		idx[key] = len(groups)
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

// pkKey returns a stable string for a primary-key tuple. fmt.Sprint
// is enough — two distinct tuples may collide on the wire format, but
// the group-by here only cares about render-time identity (the same
// PendingEdit slice that produced the key drives the row).
func pkKey(pk []any) string {
	return fmt.Sprintf("%v", pk)
}

// formatPK renders a PK tuple as `(v1, v2, ...)`. Single-column PKs
// drop the parens to keep the row header readable.
func formatPK(pk []any) string {
	if len(pk) == 1 {
		return formatValue(pk[0])
	}
	parts := make([]string, len(pk))
	for i, v := range pk {
		parts[i] = formatValue(v)
	}
	return "(" + strings.Join(parts, ", ") + ")"
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
	return truncateValue(formatEditValue(e.NewValue, e.ColumnType))
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
// statements that A5's apply helper will execute. One statement per
// column-change. Expression edits splice NewExpr inline (NOT as a $N
// parameter); Literal edits substitute the new value with the same
// $1 / $2 binding style the runtime uses.
func writeCommitDialogSQL(b *strings.Builder, v guicontext.CommitDialogView) {
	if v.Set == nil || v.Set.IsEmpty() {
		b.WriteString("(no staged edits)")
		return
	}
	stmts := BuildCommitDialogSQL(v.Set, connectionPassword(v.Conn))
	b.WriteString("BEGIN;\n")
	shown := min(len(stmts), commitDialogMaxStmts)
	for i := range shown {
		b.WriteString(highlight.Highlight(stmts[i]))
		b.WriteByte('\n')
	}
	if len(stmts) > shown {
		fmt.Fprintf(b, "... (%d more statements)\n", len(stmts)-shown)
	}
	b.WriteString("COMMIT;")
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
		sql := highlight.Highlight(truncateSQL(r.SQL))
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

// truncateSQL caps a single SQL line for the dry-run table. Wider than
// commitDialogValueWidth because SQL is dense; still leaves room for
// the `[N rows] ` prefix within an 80-col line.
func truncateSQL(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	const max = 64
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

// writeCommitDialogGate renders the apply-enable hint. On default
// connections this is a one-liner "[a] apply  [d] dry-run  [s] sql
// [Esc] cancel". On confirm_writes connections a typed-name line
// precedes it: until TypedName == Conn.Name it points at the [t]
// prompt (echoing any prior attempt), then the apply-allowed banner
// replaces it.
func writeCommitDialogGate(b *strings.Builder, v guicontext.CommitDialogView) {
	if v.Conn != nil && v.Conn.ConfirmWrites {
		if v.TypedName != v.Conn.Name {
			fmt.Fprintf(b, "[t] type %q to enable [a]: %s\n", v.Conn.Name, v.TypedName)
		} else {
			b.WriteString("typed-name match — [a] to apply\n")
		}
	}
	b.WriteString("[a] apply  [d] dry-run  [s] sql  [Esc] cancel")
}

// BuildCommitDialogSQL returns the per-column UPDATE statements the
// dialog renders in SqlPreview mode AND the apply helper executes.
// Exposed (capital B) so A5 can call it directly when wiring OnApply
// — duplicate code there would risk the rendered SQL drifting from
// the executed SQL.
//
// password is the connection profile's plaintext password (when set);
// the function scrubs it from the returned SQL strings before they
// leave the package, mirroring session.sqlPreview (ADR-28).
//
// NOTE: this is a local scrub stub. A5 (or a later cleanup) will likely
// route through a shared helper; once that lands, replace this with
// the canonical helper. Tracked under ADR-28.
func BuildCommitDialogSQL(set *models.PendingEditSet, password string) []string {
	if set == nil || set.IsEmpty() {
		return nil
	}
	out := make([]string, 0, set.Count())
	for _, e := range set.Edits() {
		stmt := renderUpdateStmt(set.Table, e)
		if password != "" {
			stmt = strings.ReplaceAll(stmt, password, "***")
		}
		out = append(out, stmt)
	}
	return out
}

// renderUpdateStmt formats one column-level UPDATE statement:
//
//	UPDATE "schema"."table" SET "col" = $1 WHERE "pk1" IS NOT DISTINCT FROM $2
//
// For Expression edits the SET clause inlines NewExpr verbatim (NO
// quoting, NO parameter binding) — this matches A5's eventual apply
// path so users see the same SQL the runtime executes.
func renderUpdateStmt(t models.Ref, e models.PendingEdit) string {
	var b strings.Builder
	b.WriteString("UPDATE ")
	b.WriteString(quoteIdent(t.Schema, t.Table))
	b.WriteString(" SET ")
	b.WriteString(quoteOne(e.Column))
	if e.Kind == models.Expression {
		b.WriteString(" = ")
		b.WriteString(e.NewExpr)
	} else {
		b.WriteString(" = $1")
	}
	// WHERE clause uses IS NOT DISTINCT FROM so NULL old-values match
	// correctly (the standard `=` predicate is NULL-unsafe). One
	// predicate per PK column; positional parameters continue at $2
	// for Literal edits (so $1 is the new value) and at $1 for
	// Expression edits (which don't consume $1).
	if len(e.PrimaryKey) > 0 {
		b.WriteString(" WHERE ")
		startParam := 2
		if e.Kind == models.Expression {
			startParam = 1
		}
		for i := range e.PrimaryKey {
			if i > 0 {
				b.WriteString(" AND ")
			}
			fmt.Fprintf(&b, "%s IS NOT DISTINCT FROM $%d", quoteOne(pkColumnPlaceholder(i)), startParam+i)
		}
	}
	return b.String()
}

// pkColumnPlaceholder returns the per-PK-column identifier the SQL
// preview uses. PendingEdit carries the PK VALUES but NOT the PK
// COLUMN NAMES — those live on the table introspection. Until the
// dialog is wired to a table-metadata source, render generic
// placeholders so the SQL shape is faithful even when names are
// unknown. A5 / Z1 will widen this once they have the introspection
// handle.
func pkColumnPlaceholder(i int) string {
	return fmt.Sprintf("pk%d", i+1)
}

// quoteIdent returns the standard SQL identifier quoting for a
// schema-qualified table reference. Embedded double quotes are
// doubled per the SQL standard.
func quoteIdent(schema, table string) string {
	if schema == "" {
		return quoteOne(table)
	}
	return quoteOne(schema) + "." + quoteOne(table)
}

// quoteOne returns a single double-quoted identifier with embedded
// quotes escaped.
func quoteOne(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
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
