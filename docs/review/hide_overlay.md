# Hide Overlay (column visibility)

## Purpose
Column-visibility toggle popup for the active result tab — checklist where the user toggles which columns of the current result grid are hidden.

## Trigger
`<leader>gH` from `RESULT_GRID` scope (`commands.ResultHideOverlay` → `ResultTabsHelper.HideOverlay()` pushes the `HIDE_OVERLAY` context).

## Visible content / inputs
- Checklist body fully assembled by `popup.HideOverlay.Body()` (helper-owned).
- Per-column visibility flag.
- Toast emitted if toggle would leave zero visible columns (`ErrMinimumOneVisible`).

## Keybindings while focused
- `j` / `<down>` / `<c-n>` — cursor down (`HideOverlayMove(+1)`)
- `k` / `<up>` / `<c-p>` — cursor up (`HideOverlayMove(-1)`)
- `<space>` — toggle column under cursor
- `<esc>` / `q` — apply hidden set + close (`HideOverlayClose`)

`<c-n>` / `<c-p>` were registered in `pkg/gui/controllers/hide_overlay_controller.go:111-125` (dbsavvy-56u.2); godoc now matches code.

## Multi-step / chaining
N/A — single-screen toggle list.

## Persisted state
- Hidden-column set persisted per `(connID, baseTable)` via `AppStateStore.SetHiddenColumns` (gated on `HasRowIdentity` — tabs without a recorded `ResultIdentity` toggle in-memory only).
- **Distinct from rail-visibility hiding** (schemas-rail `H` / `U` / `<leader>H` for system schemas). The HIDE_OVERLAY popup is COLUMN-hiding for result grids only.

## Gaps / TODOs / dead-looking code
- None known.
