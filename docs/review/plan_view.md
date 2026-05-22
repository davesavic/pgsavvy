# Plan View

## Purpose
Renders parsed `models.Plan` tree (EXPLAIN / EXPLAIN ANALYZE) in a result tab (scope `PLAN`).

## Visible state
- Depth-first walk of plan nodes, indented by depth, prefixed by tree glyph (`▼` expanded interior, `▶` collapsed interior, `─` leaf).
- Cursor marker `> ` on the active row; others `  `.
- Columns: `op  cost=N.N rows=N`; when `plan.Analyzed = true`, appends `actual_cost=N.N actual_rows=N loops=N`.
- Cost-percentile color gradient (P50/P75/P90/P95 → neutral/yellow/red/bold-red) when `theme.IsMonochrome() == false` AND visible-node count ≥ 4.
- Raw-text mode shows `plan.RawText` verbatim (through `SanitizeCellEscapes`).

## Modes / states
- Tree view (default) vs raw view (toggle).
- Per-node collapsed map.

## Keybindings (Normal only, scope `PLAN`)
- `<cr>` — toggle collapse on cursor node (no-op on leaf)
- `<c-a>` — expand all nodes
- `<c-x>` — collapse all interior nodes except root
- `H` — jump cursor to heaviest descendant by cost (DFS tie-break: first encountered; auto-expands ancestor chain)
- `o` — toggle tree ↔ raw-text view
- `j` / `k` — cursor down / up

## Mouse interactions
- None.

## Persisted state
- None (per-tab session state only).

## Status / toasts / busy
- None plan-specific (errors surface via parent tab's error display).

## Gaps / TODOs / dead-looking code
- No search within plan tree.
- No node-detail popup (just inline cost / rows columns).
- Action descriptions are English literals — `Tr.Actions.Plan*` i18n fields not yet wired.
- `H` is a fixed binding under `PLAN` scope; conflicts with vim `H` "screen top" motion are scope-isolated.
