# Decision: Pin lazygit's vendored pkg/gocui as our TUI runtime

- **ID:** dbsavvy-enn-T0-gocui-pin
- **Date:** 2026-05-17
- **Status:** ACCEPTED
- **Epic:** dbsavvy-enn (T0)

## Context

DESIGN.md §6 specifies the dbsavvy TUI is built on a gocui-shaped runtime and
explicitly requires `UpdateContentOnly` for low-flicker partial repaints (cell
buffer mutation without re-laying out views). A survey of forks turned up:

- **jroimartin/gocui** (the original) — abandoned, no `UpdateContentOnly`.
- **awesome-gocui/gocui** — community fork, no `UpdateContentOnly`.
- **jesseduffield/gocui** — same maintainer as lazygit, but the standalone
  module has fallen behind lazygit's internal copy and also lacks
  `UpdateContentOnly` at any tagged or master SHA we inspected.
- **jesseduffield/lazygit/pkg/gocui** (subpackage, vendored inside lazygit) —
  the ONLY actively maintained copy that exposes `UpdateContentOnly`, plus
  related niceties (`UpdateAsync`, `ForceFlushViewsContentOnly`, click
  bindings via `ViewMouseBinding`). lazygit's main application layer is
  migrating to tcell/v3, but `pkg/gocui` is preserved as the layout runtime.

A previous spike erroneously pinned `github.com/jesseduffield/gocui` at a
master pseudo-version that does NOT have `UpdateContentOnly`; that pin has
been dropped as part of this decision.

## Decision

1. Import the gocui runtime from **`github.com/jesseduffield/lazygit/pkg/gocui`**
   (a subpackage of the lazygit module). All `pkg/gui/**` code and `main.go`
   wiring will use this import path.
2. Keep **`github.com/jesseduffield/lazycore`** pinned independently for its
   `pkg/boxlayout` helper, which is what dbsavvy uses for window arrangement
   math (per epic D1).
3. Do **not** depend on the standalone `github.com/jesseduffield/gocui`
   module. Its require entry has been removed from `go.mod`.

## Pinned values (verbatim from go.mod)

```
github.com/jesseduffield/lazycore v0.0.0-20221012050358-03d2e40243c5
github.com/jesseduffield/lazygit v0.61.2-0.20260511142836-c49350362005
```

- The lazygit pseudo-version resolves to commit
  `c493503620051fcd2c07ac429fa3b70e5c43f13a` dated 2026-05-11T14:28:36Z.
- The lazycore pseudo-version resolves to commit
  `03d2e40243c5` dated 2022-10-12T05:03:58Z.

## Rationale

- **Only fork with `UpdateContentOnly`.** Non-negotiable per DESIGN.md §6.
- **Actively maintained.** The lazygit project ships almost daily; pkg/gocui
  receives bug fixes whenever the lazygit UI surfaces them.
- **Same upstream maintainer** as the standalone jesseduffield/gocui, so
  paradigms (managers, view rectangles, key/mouse bindings) match docs we
  already cite in DESIGN.md.
- **Transitive cost is acceptable.** Pulling lazygit drags in tcell/v3,
  go-colorful, samber/lo, kyokomi/emoji, fuzzy, etc. None of these conflict
  with our existing deps and they will mostly stay `// indirect` after T1
  lands.

## API surface verified by the compile-only spike

The spike at `cmd/spike/main.go` (build tag `spike`) successfully resolves
every symbol DESIGN.md §6 and M04 call out:

- `gocui.NewGui(NewGuiOpts) (*Gui, error)` — with `Headless: true` for
  test-mode initialization.
- `(*Gui).SetManager(...Manager)` and `(*Gui).SetManagerFunc(func(*Gui) error)`.
- `(*Gui).Update(func(*Gui) error)` — full re-layout request.
- `(*Gui).UpdateContentOnly(func(*Gui) error)` — partial repaint, required
  by DESIGN.md §6.
- `(*Gui).SetView(name string, x0, y0, x1, y1 int, overlaps byte) (*View, error)`
  — exercised with BOTH M04 shapes: zero-rect `("a", 0,0,0,0, 0)` and
  minimal-rect `("b", 0,0,1,1, 0)`.
- `(*Gui).SetKeybinding(viewname string, key Key, handler func(*Gui, *View) error) error`
  — note `Key` (struct) not `KeyName` (alias); constructed via
  `gocui.NewKeyName(gocui.KeyEnter)`.
- `(*Gui).SetViewClickBinding(*ViewMouseBinding) error` — mouse/click
  binding API. See spec amendment below.
- `(*Gui).MainLoop() error` — referenced (not invoked, to keep the spike
  compile-only and non-blocking).

Also forces a transitive on `github.com/jesseduffield/lazycore/pkg/boxlayout`
so a future `go mod tidy` keeps both pins alive.

## Spec amendment needed

DESIGN.md §6 currently references `SetMouseBinding`. **That method does not
exist** in any gocui fork — standalone, awesome-gocui, or lazygit's vendored
copy. The real API is:

```go
(*gocui.Gui).SetViewClickBinding(*gocui.ViewMouseBinding) error
```

with `ViewMouseBinding` carrying `ViewName`, `FocusedView`, `Key` (`KeyName`,
typically `gocui.MouseLeft`), `Modifier`, and `Handler func(ViewMouseBindingOpts) error`.

This is an **intent-preserving** fix — the spec wants click binding, and
this is the correct method. The orchestrator will land the DESIGN.md edit
separately; this decision doc records the intent so T1+ code can use the
real method name without ambiguity.

## Consequences

- **All T1+ work depends on this pin.** Any future epic that wants to bump
  the gocui revision must re-run the spike (or an equivalent) to re-verify
  that `UpdateContentOnly`, `SetViewClickBinding`, and the M04 SetView
  shapes still resolve.
- **Downgrading is non-trivial.** If lazygit ever drops `UpdateContentOnly`
  we would need either to (a) vendor `pkg/gocui` at the last good revision,
  or (b) replace our partial-repaint strategy. Both are scoped as a future
  decision; this one only commits us to the current revision.
- **Transitive surface grows.** tcell/v3, go-colorful v1.4.0, samber/lo
  v1.53, kyokomi/emoji/v2, sahilm/fuzzy, stefanhaller/git-todo-parser, and
  a handful of others are now in `go.sum`. None are imported directly by
  dbsavvy code; depguard rule `drivers-all` (added in the same task)
  ensures `pkg/drivers/**` cannot accidentally pull lazygit into the data
  layer.
- **Spike package lives at `cmd/spike/`, not `cmd/_spike/`** — the
  underscore-prefixed form is hidden from `go list ./...` and `go mod tidy`,
  which would silently strip the lazygit/lazycore pins on the next tidy run.
  Using `cmd/spike/` with a `//go:build spike` tag keeps the package
  visible to module tooling while excluding it from default builds.
- **Two pre-existing lint issues fixed in-scope** (user-authorized scope
  creep) so the AC's `golangci-lint run ./...` clean-exit requirement can
  hold: removed unused `profile models.Connection` field from
  `pkg/drivers/pg/session.go::Session` (declared in epic 921, never assigned
  or read); wrapped `defer c.Close()` in `pkg/drivers/pg/driver_test.go` to
  satisfy errcheck. Both fixes are isolated and `go test ./...` stays green.
