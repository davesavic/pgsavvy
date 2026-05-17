//go:build spike

// Package main is a compile-only spike to verify that the pinned lazygit
// pkg/gocui fork exposes every symbol DESIGN.md §6 depends on (notably
// UpdateContentOnly). It also forces a hard dep on lazycore/pkg/boxlayout so
// `go mod tidy` keeps that pin alive. Build with:
//
//	go build -tags=spike ./cmd/_spike/...
//
// This file is removed when the gui layer (epic dbsavvy-enn T10) lands real
// usages of the same API surface.
package main

import (
	"fmt"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	_ "github.com/jesseduffield/lazycore/pkg/boxlayout"
)

func main() {
	g, err := gocui.NewGui(gocui.NewGuiOpts{
		OutputMode:      gocui.OutputNormal,
		SupportOverlaps: false,
		Headless:        true,
		Width:           80,
		Height:          24,
	})
	if err != nil {
		fmt.Println("spike: NewGui failed:", err)
		return
	}

	// Exercise SetManagerFunc (managers are how gocui asks us to (re)lay out).
	g.SetManagerFunc(func(*gocui.Gui) error { return nil })

	// And SetManager (variadic Manager form) — same Manager surface, different shape.
	g.SetManager()

	// Update + UpdateContentOnly: DESIGN.md §6 specifically calls out
	// UpdateContentOnly for low-flicker partial repaints.
	g.Update(func(*gocui.Gui) error { return nil })
	g.UpdateContentOnly(func(*gocui.Gui) error { return nil })

	// SetView with the two M04-mandated shapes:
	//   1) zero-rect ("a", 0,0,0,0, 0)         — used as a sentinel during init
	//   2) minimal-rect ("b", 0,0,1,1, 0)      — used once dims are known
	if _, err := g.SetView("a", 0, 0, 0, 0, 0); err != nil {
		// gocui returns ErrInvalidPoint for the zero-rect case during initial
		// layout — that's expected and not fatal to the spike.
		_ = err
	}
	if _, err := g.SetView("b", 0, 0, 1, 1, 0); err != nil {
		_ = err
	}

	// SetKeybinding takes a Key (not a raw KeyName) — confirm via NewKeyName.
	if err := g.SetKeybinding("b", gocui.NewKeyName(gocui.KeyEnter), func(*gocui.Gui, *gocui.View) error {
		return nil
	}); err != nil {
		_ = err
	}

	// SetViewClickBinding is the real method (DESIGN.md §6's "SetMouseBinding"
	// is a spec typo; tracked in decisions/dbsavvy-enn-T0-gocui-pin.md).
	if err := g.SetViewClickBinding(&gocui.ViewMouseBinding{
		ViewName:    "b",
		FocusedView: "b",
		Key:         gocui.MouseLeft,
		Handler:     func(gocui.ViewMouseBindingOpts) error { return nil },
	}); err != nil {
		_ = err
	}

	// MainLoop reference — not invoked (Headless + no input would block tests).
	_ = g.MainLoop
}
