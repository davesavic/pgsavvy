package helpers

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/davesavic/dbsavvy/pkg/drivers/pg"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/session"
)

// DefaultFKForwardLimit is the shipped row-count cap for the parent-table
// SELECT issued by Jump. UserConfig.Editor.FKForwardLimit overrides it; a
// non-positive override falls back to this default.
const DefaultFKForwardLimit = 1000

// fkForwardTabLabelMax bounds the rendered tab title produced by Jump.
// Mirrors result_tabs_helper.go's resultTabLabelMax so the truncation
// matches what the tab strip can render.
const fkForwardTabLabelMax = 40

// fkForwardToastTTL is the lifetime of toasts surfaced by the helper.
const fkForwardToastTTL = 4 * time.Second

// FKCache is the narrow surface FKForwardHelper consumes for foreign-key
// metadata lookup. *session.FKCache satisfies it; tests inject a fake
// that returns canned FKs without involving a live driver.
type FKCache interface {
	Get(ctx context.Context, schema, table string) ([]models.ForeignKey, error)
}

var _ FKCache = (*session.FKCache)(nil)

// QueryRunner is the narrow runner surface FKForwardHelper consumes to
// dispatch the parameterized parent-table SELECT. *data.QueryRunner's
// RunQuery method satisfies it; tests inject a fake.
type QueryRunner interface {
	RunQuery(ctx context.Context, q models.Query) (*session.RunHandle, error)
}

// ResultTabsManager is the narrow surface FKForwardHelper uses to open
// the new result tab. *ui.ResultTabsHelper satisfies it.
type ResultTabsManager interface {
	OpenResultTab(label string, rh *session.RunHandle) error
}

// JumpList is the narrow surface FKForwardHelper uses to push a jump
// entry before opening the new tab. *ui.ResultJumpList satisfies it.
type JumpList interface {
	Push(e ui.JumpEntry)
}

// Toaster is the narrow toast surface FKForwardHelper uses for the
// "multiple FKs on column" / "tab cap reached" notifications. May be
// nil — toasts then no-op.
type Toaster interface {
	Show(message string, ttl time.Duration)
}

// BusyChecker reports whether the underlying session currently has an
// active stream. Currently unused: with last-wins (dbsavvy-lxn.1) a new
// op preempts any parked stream rather than queueing, so Jump no longer
// branches on busyness. Retained as optional plumbing; may be nil.
type BusyChecker interface {
	IsBusy() bool
}

// CurrentTab is the narrow surface FKForwardHelper consumes to inspect
// the originating tab: its identity (for the JumpList Push), the base
// table the result is drawn from, the result-set column names, and the
// row values at cursorRow.
type CurrentTab interface {
	// Slot returns the tab's 0-based slot index.
	Slot() int
	// ID returns the tab's monotonically-allocated identifier.
	ID() int64
	// BaseTable returns the (schema, table) the result is drawn from. An
	// empty schema means "no qualifier resolved"; QuoteQualified handles
	// the unqualified case gracefully.
	BaseTable() (schema string, table string)
	// ColumnNames returns the result-set column names in projection
	// order. Used to locate the cursor column AND to validate the
	// composite-FK guard (all FK columns must appear in this list).
	ColumnNames() []string
	// RowValues returns the row at index row (0-based). The boolean is
	// false when the row is not yet loaded (stream race) — Jump surfaces
	// the "row not yet loaded" guard in that case. The returned slice
	// must align positionally with ColumnNames().
	RowValues(row int) ([]any, bool)
}

// FKForwardHelper drives the `gd` forward foreign-key navigation: given
// a cursor cell on an FK column, build a parameterized SELECT against
// the referenced (parent) table, push a JumpEntry capturing the
// originating tab + cursor, and open a new result tab streaming the
// parent rows. See dbsavvy-bwq.16 (B5).
//
// The helper deliberately holds no state — every Jump call is a fresh
// lookup. Last-wins (dbsavvy-lxn.1): RunQuery preempts any parked prior
// stream at the QueryRunner chokepoint, so a later gd press preempts an
// earlier one rather than queueing behind it.
type FKForwardHelper struct {
	cache    FKCache
	jumpList JumpList
	runner   QueryRunner
	tabs     ResultTabsManager
	toast    Toaster
	busy     BusyChecker
	limit    int
}

// FKForwardDeps bundles FKForwardHelper's collaborators. cache, runner,
// and tabs are required; jumpList is required (Push is called before
// OpenResultTab so a missing list would lose history). toast and busy
// are optional.
type FKForwardDeps struct {
	Cache    FKCache
	JumpList JumpList
	Runner   QueryRunner
	Tabs     ResultTabsManager
	Toast    Toaster
	Busy     BusyChecker
	// Limit caps the parent SELECT's row count. Non-positive falls back
	// to DefaultFKForwardLimit. Wire from UserConfig.Editor.FKForwardLimit.
	Limit int
}

// NewFKForwardHelper constructs a helper. The returned value is non-nil;
// passing zero deps does NOT panic on construction — Jump nil-checks
// individual collaborators before use and returns errors describing the
// missing wire.
func NewFKForwardHelper(deps FKForwardDeps) *FKForwardHelper {
	limit := deps.Limit
	if limit <= 0 {
		limit = DefaultFKForwardLimit
	}
	return &FKForwardHelper{
		cache:    deps.Cache,
		jumpList: deps.JumpList,
		runner:   deps.Runner,
		tabs:     deps.Tabs,
		toast:    deps.Toast,
		busy:     deps.Busy,
		limit:    limit,
	}
}

// errors surfaced by Jump. Callers may use errors.Is to branch on the
// guard kind, but the error message itself is the user-facing reason
// (no further translation is required).
var (
	// ErrFKNotFound is returned when no foreign key on the base table
	// covers the cursor column.
	ErrFKNotFound = errors.New("fk forward: no foreign key on cursor column")
	// ErrFKValueNull is returned when the cursor cell is NULL.
	ErrFKValueNull = errors.New("fk forward: foreign-key value is NULL")
	// ErrRowNotLoaded is returned when the cursor row is past the
	// loaded tail (stream race).
	ErrRowNotLoaded = errors.New("fk forward: row not yet loaded")
	// ErrCompositeMissingColumns is returned when the cursor's FK is
	// composite and some of the other columns are absent from the
	// current result projection.
	ErrCompositeMissingColumns = errors.New("fk forward: composite FK is missing columns in result")
)

// Jump implements the `gd` flow. Returns a non-nil error when one of
// the guards (no FK / NULL cell / row not loaded / composite missing
// columns) trips, OR when a downstream dependency (runner / tabs)
// surfaces an error. A successful Jump returns nil after the new tab
// has been opened (preempting any parked prior stream; dbsavvy-lxn.1).
//
// Guard order: invalid arguments → cache lookup → FK match → composite
// columns → cursor row → cursor cell. We do the cheapest checks first
// so a guard miss does not hit the FK cache loader unnecessarily.
func (h *FKForwardHelper) Jump(ctx context.Context, tab CurrentTab, cursorRow, cursorCol int) error {
	if h == nil {
		return errors.New("fk forward: helper is nil")
	}
	if tab == nil {
		return errors.New("fk forward: current tab is nil")
	}
	if h.cache == nil {
		return errors.New("fk forward: cache is not wired")
	}
	if h.runner == nil {
		return errors.New("fk forward: runner is not wired")
	}
	if h.tabs == nil {
		return errors.New("fk forward: tabs is not wired")
	}
	if h.jumpList == nil {
		return errors.New("fk forward: jump list is not wired")
	}

	cols := tab.ColumnNames()
	if cursorCol < 0 || cursorCol >= len(cols) {
		return fmt.Errorf("fk forward: cursor column %d out of range [0,%d)", cursorCol, len(cols))
	}
	cursorColName := cols[cursorCol]

	baseSchema, baseTable := tab.BaseTable()
	if baseTable == "" {
		return errors.New("fk forward: current result has no base table")
	}

	// Cache lookup. Loader errors are surfaced directly — the caller can
	// retry by re-pressing gd.
	fks, err := h.cache.Get(ctx, baseSchema, baseTable)
	if err != nil {
		return fmt.Errorf("fk forward: load fks for %s.%s: %w", baseSchema, baseTable, err)
	}

	// Find FK(s) whose Columns includes cursorColName. When multiple
	// match the same column, choose the first by FK.Name lex order and
	// surface an informational toast (DESIGN.md §12.6 amendment).
	matches := matchingFKs(fks, cursorColName)
	if len(matches) == 0 {
		return fmt.Errorf("%w: %s", ErrFKNotFound, cursorColName)
	}
	chosen := matches[0]
	if len(matches) > 1 {
		h.emitToast(fmt.Sprintf("multiple FKs on column %q; using %s", cursorColName, chosen.Name))
	}

	// Composite-FK guard: every column of the chosen FK must appear in
	// the current result projection so we can bind every parameter.
	colIdx := make(map[string]int, len(cols))
	for i, n := range cols {
		colIdx[n] = i
	}
	missing := make([]string, 0)
	for _, c := range chosen.Columns {
		if _, ok := colIdx[c]; !ok {
			missing = append(missing, c)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: FK %s requires columns %s in the result",
			ErrCompositeMissingColumns, chosen.Name, strings.Join(missing, ", "))
	}

	// Row guard: the row must be loaded before we can extract values.
	values, ok := tab.RowValues(cursorRow)
	if !ok {
		return ErrRowNotLoaded
	}
	if len(values) < len(cols) {
		// A short row indicates the same stream race the "not loaded"
		// guard covers; treat it as the same condition.
		return ErrRowNotLoaded
	}

	// NULL guard: refuse the jump when ANY FK column value is NULL.
	// PostgreSQL treats NULL = NULL as unknown, so the lookup would
	// always return zero rows; surface the dedicated guard so the user
	// understands why.
	args := make([]any, 0, len(chosen.Columns))
	for _, c := range chosen.Columns {
		v := values[colIdx[c]]
		if v == nil {
			return fmt.Errorf("%w: column %s", ErrFKValueNull, c)
		}
		args = append(args, v)
	}

	// Build the parameterized SELECT. ALL identifiers go through
	// QuoteIdent / QuoteQualified per ADR-21.
	sql := buildFKForwardSQL(chosen, h.limit)
	q := models.Query{SQL: sql, Args: args}

	// Push the jump entry BEFORE running. If RunQuery / OpenResultTab
	// fail we still want the entry in the list so the user can <C-o>
	// back (the originating cell hasn't moved).
	h.jumpList.Push(ui.JumpEntry{
		TabSlot: tab.Slot(),
		TabID:   fmt.Sprintf("%d", tab.ID()),
		Row:     cursorRow,
		Col:     cursorCol,
		At:      time.Now(),
	})

	// Last-wins (dbsavvy-lxn.1): RunQuery preempts any parked prior stream
	// at the QueryRunner chokepoint before running, mirroring run/run_all.
	// fk-forward therefore does NOT queue behind an active stream, so no
	// "queued" toast is emitted — the preempt is silent, matching run_all.
	rh, err := h.runner.RunQuery(ctx, q)
	if err != nil {
		return fmt.Errorf("fk forward: run parent select: %w", err)
	}

	label := buildFKForwardLabel(chosen, args)
	if err := h.tabs.OpenResultTab(label, rh); err != nil {
		// ErrTabCapReached: surface the "unpin a tab" guidance via the
		// toast. Caller still gets the error so disabling-tests pass.
		if errors.Is(err, ui.ErrTabCapReached) {
			h.emitToast("result tabs at cap; unpin a tab to free a slot")
		}
		return fmt.Errorf("fk forward: open result tab: %w", err)
	}
	return nil
}

// matchingFKs returns every FK in fks whose Columns slice contains
// colName, sorted by FK.Name (lex ascending) so the caller can choose
// the first deterministically. The returned slice is a fresh allocation.
func matchingFKs(fks []models.ForeignKey, colName string) []models.ForeignKey {
	out := make([]models.ForeignKey, 0, 2)
	for _, fk := range fks {
		if slices.Contains(fk.Columns, colName) {
			out = append(out, fk)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// buildFKForwardSQL composes the parameterized SELECT against the
// referenced table. All identifiers (schema, table, columns) are
// quoted via pg.QuoteIdent / pg.QuoteQualified to defeat injection
// AND to round-trip mixed-case / reserved-word names.
func buildFKForwardSQL(fk models.ForeignKey, limit int) string {
	var b strings.Builder
	b.WriteString("SELECT * FROM ")
	b.WriteString(pg.QuoteQualified(fk.RefSchema, fk.RefTable))
	b.WriteString(" WHERE ")
	for i, col := range fk.RefColumns {
		if i > 0 {
			b.WriteString(" AND ")
		}
		b.WriteString(pg.QuoteIdent(col))
		fmt.Fprintf(&b, "=$%d", i+1)
	}
	fmt.Fprintf(&b, " LIMIT %d", limit)
	return b.String()
}

// buildFKForwardLabel renders the tab title. For a simple FK we render
// `→ <ref_table>(<pk_value>)`; for a composite FK we render
// `→ <ref_table>(col1=val1, col2=val2, ...)`. Both shapes are truncated
// at fkForwardTabLabelMax with an ellipsis suffix when the rendered
// body overruns.
func buildFKForwardLabel(fk models.ForeignKey, args []any) string {
	var inner string
	if len(fk.RefColumns) == 1 && len(args) == 1 {
		inner = fmt.Sprintf("%v", args[0])
	} else {
		parts := make([]string, 0, len(fk.RefColumns))
		for i, c := range fk.RefColumns {
			if i >= len(args) {
				break
			}
			parts = append(parts, fmt.Sprintf("%s=%v", c, args[i]))
		}
		inner = strings.Join(parts, ", ")
	}
	full := fmt.Sprintf("→ %s(%s)", fk.RefTable, inner)
	return truncateLabel(full, fkForwardTabLabelMax)
}

// truncateLabel clips s so the rendered string is no more than max
// bytes long, appending an ellipsis when truncation occurred. The
// ellipsis ("…") is 3 bytes; budget accounts for it. The label is
// purely cosmetic, so byte-level (rather than rune-level) clipping is
// acceptable — the tab-strip layout treats the title as opaque bytes.
func truncateLabel(s string, max int) string {
	if max <= 0 {
		return s
	}
	if len(s) <= max {
		return s
	}
	const ellipsis = "…"
	if max <= len(ellipsis) {
		return s[:max]
	}
	return s[:max-len(ellipsis)] + ellipsis
}

func (h *FKForwardHelper) emitToast(msg string) {
	if h.toast == nil {
		return
	}
	h.toast.Show(msg, fkForwardToastTTL)
}
