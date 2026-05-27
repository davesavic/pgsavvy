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
(`users.id` / `posts.id` both shown as `id`); ordering by name is ambiguous or
errors, while ordinal targets the exact displayed column. The subquery wrapper
also makes joins, CTEs, and any pre-existing `ORDER BY` irrelevant. Clearing the
sort (3rd cycle) re-runs `orig` unchanged.

### Sort action flow (shared by `<leader>s` picker and header double-click)

1. Guard — tab must be a single result-returning statement (reuse
   `query.DetectFromQuery`); else toast, no-op.
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
