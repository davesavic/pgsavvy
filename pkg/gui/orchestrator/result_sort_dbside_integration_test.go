//go:build integration

// Package orchestrator_test (integration) end-to-end verifies the
// database-side result-sort flow (epic dbsavvy-72k) against the live docker
// Postgres fixture. The unit tests (sort_sql_test.go,
// result_tabs_helper_sort_flow_test.go, result_tabs_helper_rerun_test.go)
// prove the cycle / guard / wrapSorted string-building logic in isolation;
// THIS test proves the wrapped ORDER-BY-by-ordinal SQL actually executes on a
// real Postgres backend, orders the FULL result set from the top, survives a
// duplicate column name in a join, re-binds a $1 parameter on the re-run, and
// tolerates a trailing line comment.
//
// How a sort is triggered (production command path, not a shortcut):
//
//	runCommand(commands.ResultSortPick) -> ResultTabsController handler ->
//	ResultTabsHelper.SortPick() -> g.choiceHelp.Choose(...) opens the SELECTION
//	picker and records the onSubmit callback. The test then submits a column
//	choice via g.ChoiceHelperForTest().Submit(idx), which fires onSubmit(idx) ->
//	helper.onSortRequest(idx) -> QueryEditorController.sortActiveResult(idx) ->
//	SortActiveTab (guards + asc->desc->clear cycle + wrapSorted) ->
//	reRunActiveTab -> QueryRunner.RunQuery against the live session. This is the
//	exact wiring a real <leader>s keypress drives; only the final "pick this
//	row" keystroke is replaced by the programmatic Submit (the recorder driver
//	used by setupQuerySmoke is not the serialized driver, so a FeedKey dance on
//	the SELECTION popup is out of scope — Submit invokes the identical onSubmit
//	closure the SelectionController's <cr> handler would).
//
// How display order is read: sorting is DB-side (dbsavvy-72k.6: "The grid no
// longer reorders rows for sort; ordering is DB-side"), so the grid's row
// buffer order IS the display order. tab.Grid().AllRows() returns that buffer
// verbatim — no new accessor is needed.
//
// How the INITIAL result tab is opened: directly through the live runner
// (bag.QueryRunner.RunQuery + helper.OpenResultTab + AttachActiveTabOrigin) —
// the same seam the editor run path uses internally (openResultTab) — NOT via
// the editor <leader>r (commands.QueryRun) path. The reason is scenario3: the
// editor run path executes the raw statementUnderCursor() text and has no way
// to bind a $1 parameter, whereas a parameterized initial tab (Args: {20}) is
// exactly what scenario3 must open before sorting. Routing every scenario's tab
// through the one openTabDirect seam keeps the harness uniform. (The earlier
// seedEditor "no statement under cursor" breakage that first forced this is now
// fixed — dbsavvy-72k.9 — but the Args-binding constraint stands.) The SORT
// itself — the actual subject of .8 — still runs the full production command
// path (ResultSortPick -> SortPick -> Submit -> sortActiveResult ->
// reRunActiveTab -> RunQuery).
//
// Reuses setupQuerySmoke / requireSmokePG / runCommand / eventuallyQE from
// query_execution_smoke_integration_test.go + tui_smoke_integration_test.go
// (same orchestrator_test package).
package orchestrator_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// sortScratchDDL creates a self-contained scratch schema with two tables that
// BOTH expose an "id" column, so a join SELECT * surfaces a duplicate column
// name. wrapSorted sorts by ordinal (never by name), which is precisely the
// behaviour live PG must confirm. Idempotent (DROP ... CASCADE first), cleaned
// up by the caller.
const sortScratchDDL = `
DROP SCHEMA IF EXISTS dbsavvy_sort_it CASCADE;
CREATE SCHEMA dbsavvy_sort_it;
CREATE TABLE dbsavvy_sort_it.a (id BIGINT PRIMARY KEY, label TEXT NOT NULL);
CREATE TABLE dbsavvy_sort_it.b (id BIGINT PRIMARY KEY, a_id BIGINT NOT NULL, note TEXT NOT NULL);
INSERT INTO dbsavvy_sort_it.a (id, label) VALUES
    (10, 'ten'), (30, 'thirty'), (20, 'twenty'), (40, 'forty'), (5, 'five');
INSERT INTO dbsavvy_sort_it.b (id, a_id, note) VALUES
    (101, 10, 'n1'), (103, 30, 'n3'), (102, 20, 'n2'), (104, 40, 'n4'), (105, 5, 'n5');
`

// openSortScratch creates the scratch schema via a standalone pgx connection
// and registers a t.Cleanup that drops it. The returned conn stays open for
// reference ORDER BY queries; its Close is also deferred via Cleanup.
func openSortScratch(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("pgx.Connect: %v", err)
	}
	if _, err := conn.Exec(ctx, sortScratchDDL); err != nil {
		_ = conn.Close(ctx)
		t.Fatalf("scratch DDL: %v", err)
	}
	t.Cleanup(func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer dcancel()
		_, _ = conn.Exec(dctx, "DROP SCHEMA IF EXISTS dbsavvy_sort_it CASCADE")
		_ = conn.Close(dctx)
	})
	return conn
}

// refColumn issues sql (with optional args) on the reference pgx connection and
// returns the value of result column index col (0-based) for every row, in
// returned order, formatted with %v so it can be compared against the grid's
// any-typed cell values formatted the same way.
func refColumn(t *testing.T, conn *pgx.Conn, col int, sql string, args ...any) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := conn.Query(ctx, sql, args...)
	if err != nil {
		t.Fatalf("reference query %q: %v", sql, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			t.Fatalf("reference Values: %v", err)
		}
		if col < 0 || col >= len(vals) {
			t.Fatalf("reference col %d out of range (%d cols)", col, len(vals))
		}
		out = append(out, fmt.Sprintf("%v", vals[col]))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reference rows.Err: %v", err)
	}
	return out
}

// gridColumn reads the grid's buffered rows in DISPLAY order (== buffer order,
// since sort is DB-side) and returns column index col formatted with %v.
func gridColumn(t *testing.T, tab *ui.Tab, col int) []string {
	t.Helper()
	g := tab.Grid()
	if g == nil {
		t.Fatal("tab has no grid")
	}
	rows := g.AllRows()
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if col < 0 || col >= len(r.Values) {
			t.Fatalf("grid col %d out of range (%d cols) row=%v", col, len(r.Values), r.Values)
		}
		out = append(out, fmt.Sprintf("%v", r.Values[col]))
	}
	return out
}

func eqStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// triggerSort drives the production sort command path for the active tab and
// picks column index col in the picker. Returns after the picker submit (which
// synchronously launches the re-run). col is the RAW grid column ordinal the
// user double-clicks / picks (0-based).
func triggerSort(t *testing.T, s *queryExecutionSmoke, col int) {
	t.Helper()
	// Open the column picker (SELECTION popup) via the registered command.
	runCommand(t, s.g, commands.ResultSortPick)
	ch := s.g.ChoiceHelperForTest()
	if ch == nil || !ch.Active() {
		t.Fatalf("sort picker did not open (ChoiceHelper.Active=false)")
	}
	if got := len(ch.Choices()); col >= got {
		t.Fatalf("picker has %d choices; cannot pick col %d", got, col)
	}
	// Submit fires onSubmit(col) -> onSortRequest -> sortActiveResult ->
	// reRunActiveTab; same closure the SelectionController <cr> handler calls.
	if err := ch.Submit(col); err != nil {
		t.Fatalf("ChoiceHelper.Submit(%d): %v", col, err)
	}
}

// openTabDirect opens a result tab for sql (with optional args + defaultSchema)
// through the live runner and the production helper seam, then records the
// origin so the sort re-run can reissue the exact query. This mirrors what the
// editor run path's openResultTab does internally; it is used here because the
// editor <leader>r path cannot bind $1 Args for scenario3 (see file doc). The returned tab
// is the active tab. defaultSchema is "" for the scratch tests (schema-qualified
// SQL needs no search_path).
func openTabDirect(t *testing.T, s *queryExecutionSmoke, helper *ui.ResultTabsHelper, sql string, args []any) *ui.Tab {
	t.Helper()
	for helper.Count() > 0 {
		if err := helper.CloseActive(); err != nil {
			t.Fatalf("pre-open CloseActive: %v", err)
		}
	}
	bag := s.g.HelperBagForTest()
	octx, ocancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer ocancel()
	rh, err := bag.QueryRunner.RunQuery(octx, models.Query{SQL: sql, Args: args})
	if err != nil {
		t.Fatalf("initial RunQuery(%q): %v", sql, err)
	}
	if err := helper.OpenResultTab("q", rh); err != nil {
		t.Fatalf("OpenResultTab: %v", err)
	}
	helper.AttachActiveTabOrigin(sql, args, "")
	tab := helper.Active()
	if tab == nil {
		t.Fatal("Active() = nil after OpenResultTab")
	}
	return tab
}

// waitRows polls until the active tab buffers exactly want rows (and is not in
// an error state), or the deadline elapses.
func waitRows(t *testing.T, tab *ui.Tab, want int) {
	t.Helper()
	if !eventuallyQE(t, 5*time.Second, func() bool {
		return tab.RowCount() == int64(want)
	}) {
		t.Fatalf("RowCount = %d, want %d (state=%v err=%v)",
			tab.RowCount(), want, tab.State(), tab.Err())
	}
	if tab.Err() != nil {
		t.Fatalf("tab errored: %v", tab.Err())
	}
}

// TestResultSortDBSide_AC is the live-PG capstone for the database-side sort
// flow. Each subtest exercises one PRIORITY scenario from dbsavvy-72k.8.
func TestResultSortDBSide_AC(t *testing.T) {
	s := setupQuerySmoke(t)
	conn := openSortScratch(t, s.dsn)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Connect through the real bag.Connect so wireQueryRuntime binds the
	// QueryRunner onto the live session (mirrors TestQueryExecutionEpic_AC).
	profile := models.Connection{Name: s.connID, Driver: "postgres", DSN: s.dsn}
	s.connections = []models.Connection{profile}
	bag := s.g.HelperBagForTest()
	if bag.Connect == nil {
		t.Fatal("HelperBag.Connect is nil")
	}
	if err := bag.Connect.Connect(ctx, &profile); err != nil {
		t.Fatalf("bag.Connect.Connect: %v", err)
	}
	if !bag.QueryRunner.HasSession() {
		t.Fatal("QueryRunner.HasSession() = false after Connect")
	}
	ensureLogView(t, s.rec)

	helper := s.g.ResultTabsHelper()
	if helper == nil {
		t.Fatal("ResultTabsHelper not wired")
	}

	t.Run("scenario1_join_sorted_by_ordinal_equals_direct_order_by", func(t *testing.T) {
		// A join that selects two "id" columns (a.id and b.id). SELECT *
		// surfaces both as "id" — sorting by name would be ambiguous; wrapSorted
		// uses the ordinal, so this proves the ordinal wrap runs on real PG and
		// orders the FULL set from the top.
		const join = "SELECT a.id, a.label, b.id, b.note FROM dbsavvy_sort_it.a a JOIN dbsavvy_sort_it.b b ON b.a_id = a.id"
		tab := openTabDirect(t, s, helper, join, nil)
		waitRows(t, tab, 5)

		// Sort ASC by the FIRST column (ordinal 1 = a.id).
		triggerSort(t, s, 0)
		waitRows(t, tab, 5)

		gotCol0 := gridColumn(t, tab, 0)
		// Reference: wrap the SAME join in a derived table and ORDER BY ordinal 1
		// ASC — exactly what wrapSorted builds (modulo the _dbsavvy_sort alias).
		wantCol0 := refColumn(t, conn, 0,
			"SELECT * FROM ("+join+") _x ORDER BY 1 ASC")
		if !eqStrings(gotCol0, wantCol0) {
			t.Fatalf("ASC col0 display order = %v, want %v (direct ORDER BY 1)", gotCol0, wantCol0)
		}
		// Sanity: ascending a.id is 5,10,20,30,40.
		want := []string{"5", "10", "20", "30", "40"}
		if !eqStrings(gotCol0, want) {
			t.Fatalf("ASC col0 = %v, want %v", gotCol0, want)
		}

		// Sort by the THIRD column (ordinal 3 = b.id), proving the ordinal — not
		// the name "id" — selects the sort key.
		triggerSort(t, s, 2)
		waitRows(t, tab, 5)
		gotCol2 := gridColumn(t, tab, 2)
		wantCol2 := refColumn(t, conn, 2,
			"SELECT * FROM ("+join+") _x ORDER BY 3 ASC")
		if !eqStrings(gotCol2, wantCol2) {
			t.Fatalf("ASC col2 (b.id) display order = %v, want %v", gotCol2, wantCol2)
		}
		if want := []string{"101", "102", "103", "104", "105"}; !eqStrings(gotCol2, want) {
			t.Fatalf("ASC col2 = %v, want %v", gotCol2, want)
		}

		if err := helper.CloseActive(); err != nil {
			t.Fatalf("CloseActive: %v", err)
		}
	})

	t.Run("scenario2_cycle_asc_desc_clear", func(t *testing.T) {
		const q = "SELECT id, label FROM dbsavvy_sort_it.a"
		tab := openTabDirect(t, s, helper, q, nil)
		waitRows(t, tab, 5)

		// Capture the ORIGINAL (unsorted) order so the clear assertion can
		// restore-compare. The DB returns rows in physical/scan order with no
		// ORDER BY; capture whatever that is.
		orig := refColumn(t, conn, 0, q)

		// asc by col 0 (id).
		triggerSort(t, s, 0)
		waitRows(t, tab, 5)
		asc := gridColumn(t, tab, 0)
		if want := []string{"5", "10", "20", "30", "40"}; !eqStrings(asc, want) {
			t.Fatalf("asc = %v, want %v", asc, want)
		}

		// desc — reversed.
		triggerSort(t, s, 0)
		waitRows(t, tab, 5)
		desc := gridColumn(t, tab, 0)
		if want := []string{"40", "30", "20", "10", "5"}; !eqStrings(desc, want) {
			t.Fatalf("desc = %v, want %v", desc, want)
		}

		// clear — original order restored (origSQL re-run verbatim).
		triggerSort(t, s, 0)
		waitRows(t, tab, 5)
		cleared := gridColumn(t, tab, 0)
		if !eqStrings(cleared, orig) {
			t.Fatalf("cleared order = %v, want original %v", cleared, orig)
		}

		if err := helper.CloseActive(); err != nil {
			t.Fatalf("CloseActive: %v", err)
		}
	})

	t.Run("scenario3_parameterized_query_sort_rebinds_arg", func(t *testing.T) {
		// Open the parameterized tab with $1 bound; the origin carries the arg.
		// The SORT flows through the production path (SortPick -> Submit ->
		// sortActiveResult -> reRunActiveTab), and reRunActiveTab re-binds
		// origArgs from ActiveTabOrigin — that re-bind on the WRAPPED re-run is
		// exactly what this scenario proves on live PG (a lost binding surfaces
		// as "$1 unbound").
		const q = "SELECT id, label FROM dbsavvy_sort_it.a WHERE id >= $1"
		const arg = int64(20)
		tab := openTabDirect(t, s, helper, q, []any{arg})
		// id >= 20 -> {20,30,40} = 3 rows.
		waitRows(t, tab, 3)

		// Sort asc by id. The re-run wraps the $1 query AND must re-bind arg=20;
		// a lost binding surfaces as "$1 unbound" -> tab error / 0 rows.
		triggerSort(t, s, 0)
		waitRows(t, tab, 3)
		if tab.Err() != nil {
			t.Fatalf("parameterized sort errored (arg not re-bound?): %v", tab.Err())
		}
		got := gridColumn(t, tab, 0)
		want := refColumn(t, conn, 0,
			"SELECT * FROM ("+q+") _x ORDER BY 1 ASC", arg)
		if !eqStrings(got, want) {
			t.Fatalf("parameterized ASC = %v, want %v", got, want)
		}
		if want := []string{"20", "30", "40"}; !eqStrings(got, want) {
			t.Fatalf("parameterized ASC = %v, want %v", got, want)
		}

		if err := helper.CloseActive(); err != nil {
			t.Fatalf("CloseActive: %v", err)
		}
	})

	t.Run("scenario5_inner_limit_hoisted_sorts_full_set", func(t *testing.T) {
		// Regression for dbsavvy-af3: a browse query carries its own LIMIT. If the
		// LIMIT stays INSIDE the sort wrapper, Postgres applies it to the unordered
		// inner scan and the outer ORDER BY sorts only an arbitrary subset — so the
		// true minimum (here id=5) can be missing entirely. wrapSorted must hoist
		// the LIMIT out past the ORDER BY so the FULL set is sorted, then limited.
		// Table a has 5 rows {5,10,20,30,40}; LIMIT 3 must yield the 3 SMALLEST.
		const q = "SELECT id, label FROM dbsavvy_sort_it.a LIMIT 3"
		tab := openTabDirect(t, s, helper, q, nil)
		waitRows(t, tab, 3)

		triggerSort(t, s, 0)
		waitRows(t, tab, 3)
		if tab.Err() != nil {
			t.Fatalf("inner-limit sort errored: %v", tab.Err())
		}
		got := gridColumn(t, tab, 0)
		// The 3 smallest ids in ascending order — proves the LIMIT was applied
		// AFTER the sort, not to an arbitrary unordered subset.
		if want := []string{"5", "10", "20"}; !eqStrings(got, want) {
			t.Fatalf("inner-limit ASC = %v, want %v (LIMIT must apply after ORDER BY)", got, want)
		}

		if err := helper.CloseActive(); err != nil {
			t.Fatalf("CloseActive: %v", err)
		}
	})

	t.Run("scenario4_trailing_line_comment_sort_no_error", func(t *testing.T) {
		// Statement ending in a trailing line comment + a trailing ';'. wrapSorted
		// TrimRight-strips the ';' and emits a newline before the closing paren so
		// the "-- c" comment cannot swallow the injected ORDER BY. A naive wrap
		// would produce "... -- c) _dbsavvy_sort ORDER BY 1 ASC" (the ORDER BY
		// commented out / syntax error). This proves the real wrap survives.
		const q = "SELECT id, label FROM dbsavvy_sort_it.a -- trailing comment\n;"
		tab := openTabDirect(t, s, helper, q, nil)
		waitRows(t, tab, 5)

		triggerSort(t, s, 0)
		waitRows(t, tab, 5)
		if tab.Err() != nil {
			t.Fatalf("trailing-comment sort errored: %v", tab.Err())
		}
		if tab.State() == ui.StateError {
			t.Fatalf("trailing-comment sort tab in StateError")
		}
		got := gridColumn(t, tab, 0)
		if want := []string{"5", "10", "20", "30", "40"}; !eqStrings(got, want) {
			t.Fatalf("trailing-comment ASC = %v, want %v", got, want)
		}

		if err := helper.CloseActive(); err != nil {
			t.Fatalf("CloseActive: %v", err)
		}
	})

	if err := s.g.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
