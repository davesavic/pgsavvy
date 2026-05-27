# Database-side result sorting (ORDER BY re-run)

Date: 2026-05-27

## Problem

Result-grid sorting is client-side (`pkg/gui/grid/sort.go`): `applySort` reorders
only the row indices currently in the grid's buffer. Two defects follow:

1. **Partial-buffer ordering.** Rows stream into the grid; sorting before the
   stream completes orders only the loaded subset. The row you want may not be
   loaded yet, so it never appears.
2. **Viewport not reset on sort.** Applying a sort leaves the cursor/scroll where
   it was, so the viewport follows the cursor's row instead of showing the top of
   the newly-sorted order. Reproduced: scroll to bottom, sort by title ascending,
   top row was "Post #200", not the alphabetically-first title. This is the
   observed "id=1 is missing from the top" symptom.

A separate, real expanded-mode cursor bug (`renderExpanded` treated the raw
`cursorRow` as a projected position) was found and fixed during investigation; it
is retained because it is on the still-client-side **filter** path.

## Decision

Sorting re-runs the originating statement wrapped in an `ORDER BY`, so the whole
result set is ordered at the database and loaded from the true top. Authoritative
ordering (collation / NULL / type), correct on a partial buffer, scales to any
future pagination.

## Design

### Ownership & data flow

- **Sort state moves to the `Tab`.** Authoritative `(columnIndex, dir)` lives on
  the result tab. The grid keeps only a *display* indicator. Required because the
  grid has no access to the SQL or the runner — only the controller does.
- **Retain SQL per tab.** The originating statement text + `Args` are stored on
  the `Tab` at open time (today `RunHandle.stmt` is private; the Tab keeps only
  `ResultIdentity`). This is what a re-run wraps.

### SQL wrap

`wrapSorted(orig, ordinal1Based, dir)`:

```sql
SELECT * FROM (
  <orig, trailing ';' stripped>
) _dbsavvy_sort
ORDER BY <ordinal> ASC|DESC
```

By **ordinal**, not name: with a join, two tables can expose the same column name
(`users.id` / `posts.id` both shown as `id`); ordering by *name* is ambiguous
(`ORDER BY id` → PG 42P10 "ORDER BY \"id\" is ambiguous"), while an *ordinal*
targets the exact displayed column with no name resolution. (Note: a derived
table that merely *exposes* duplicate output names is NOT itself an error in
Postgres — verified on PG17 that `SELECT * FROM (SELECT u.id, p.id …) x ORDER BY
1` executes fine; the ambiguity only fires on a name *reference*. So no column
aliasing is needed in the wrapper.) The subquery wrapper also makes joins, CTEs,
and any pre-existing `ORDER BY` irrelevant. Clearing the sort (3rd cycle) re-runs
`orig` unchanged.

### Sort action flow (shared by `<leader>s` picker and header double-click)

1. Guard — the active tab must hold a result grid with ≥1 column
   (`tab.Grid() != nil && grid.ColumnCount() >= 1`); else toast, no-op.
   **Do NOT use `query.DetectFromQuery`** for this gate — it rejects
   joins/aggregates/CTEs (the very results we must be able to sort).
   `DetectFromQuery` is for editability/row-identity only.
2. Guard — no unsaved staged edits; else toast
   "commit or discard edits before sorting" (re-run discards the buffer).
3. Cycle dir for the chosen column (asc → desc → clear).
4. Build SQL (wrapped, or original on clear).
5. Execute → new `RunHandle`; reset the tab's grid; `startStreaming(tab)` re-uses
   the existing stream path (`result_tabs_helper.go:1334`, already anticipates
   "a re-run in the same tab").
6. Fresh stream starts at the top (rowOffset 0, cursor 0) — id=1 lands at top.
   Set the grid's display indicator to `(col, dir)` after columns attach (note:
   `SetColumns` resets it, so the controller re-applies post-attach). A failed
   re-run surfaces as a normal query error and shows no sort indicator.

### Grid changes

- Remove the `applySort` step from `project()` (`projection.go`):
  `filter → hide`. Rows arrive DB-ordered, so render/navigation in arrival order
  is sorted order.
- `projectionLocked` / `projectedPos` and the dr6 cursor-navigation work **stay**
  — still needed for the client-side filter (which reorders/narrows). The
  expanded-mode fix stays.
- Grid `sortState` becomes **display-only** (title suffix only). Cycling logic
  moves to the Tab; grid gains `SetSortIndicator(col, dir)`.
- Header double-click (`view.go:897`) stops calling `v.SetSort` directly; add an
  `OnSortRequest(col int)` callback wired like `SetOnNearTail`, so both entry
  points run the one Tab-level flow.

### Dead code removal

Client-side sort is fully replaced (non-SELECT queries disable sort rather than
falling back). Delete `applySort`, `comparatorFor`, `compareInt/Float/Time/String`,
`toInt64`, `toFloat64`, the `pgOID*` constants, and their tests.

### Decisions taken without separate confirmation

- Non-result queries (non-SELECT / multi-statement) → sort disabled with a toast.
- Filter and hide stay client-side, applied on top of the DB-sorted rows.
- Query params (`q.Args`) forwarded unchanged on re-run.

## Testing

- Unit: `wrapSorted` — plain, join (ambiguous duplicate column), trailing `;`,
  clear-returns-original.
- Unit: sortability guard + pending-edits guard (toast, no re-run).
- Unit: grid `project()` no longer reorders; display indicator still renders.
- Integration (live PG): join query, sort by ordinal, assert order matches the DB.
- tmux: original repro — sort id↑ ⇒ id=1 at top.

## Amendments (review-plan, 2026-05-27 — bd epic dbsavvy-72k)

A `/review-plan` pass (5 critics + live PG17 checks) found blockers in the
re-run mechanism; the following supersede the body above where they conflict:

- **Re-run via `QueryRunner.RunQuery`, not a hand-rolled `Stream`+`startStreaming`.**
  Step 5's "`startStreaming(tab)` re-uses the existing stream path" is wrong:
  `startStreaming` reuses taskKey `result_tab_<id>`, which `NewQueryTask` dedupes
  to a **no-op** while the prior task is running (`result_buffer_manager.go:225`)
  — so a mid-stream sort would silently do nothing. Hand-rolling `Stream` also
  bypasses `preemptInFlight`, risking the known `streamMu` deadlock. Route through
  `QueryRunner.RunQuery` (preempts the in-flight stream, binds Args + DefaultSchema).
- **Retain SQL + Args + `DefaultSchema`** on the Tab (not just SQL): `RunHandle.stmt`
  has no Args, and the normal run path drops Args; `DefaultSchema` (search_path)
  affects unqualified-name resolution on re-run.
- **Mid-stream sort stops & discards** the prior stream, then re-runs from the top.
- **Read-only while sorted:** recompute `ResultIdentity` from the SQL actually run
  (wrapped → `HasRowIdentity=false`; orig → editable) and re-attach it on every
  re-run; clearing restores editability.
- **Hide-cols persist** against the *original* identity (re-seed after the re-run's
  `SetColumns`), since the wrapped identity has no `BaseTable`.
- **`sorting…` affordance** during re-run latency (a DB round-trip replaces the
  instant in-memory reorder).
- **`wrapSorted` must inject a newline** after `<orig>` before the `)` (else a
  trailing line-comment eats the `ORDER BY`), and strip the trailing `;` in a
  string-literal-safe way.
