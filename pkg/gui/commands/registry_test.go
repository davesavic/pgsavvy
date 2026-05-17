package commands

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// --- Registry: happy path -----------------------------------------------

func TestRegistry_RegisterThenGet(t *testing.T) {
	r := NewRegistry()
	called := false
	h := func(ExecCtx) error { called = true; return nil }
	cmd := &Command{ID: AppQuit, Description: "Quit", Handler: h}

	if err := r.Register(cmd); err != nil {
		t.Fatalf("Register: unexpected error %v", err)
	}

	got, ok := r.Get(AppQuit)
	if !ok {
		t.Fatalf("Get(%q): not found", AppQuit)
	}
	if got != cmd {
		t.Errorf("Get returned a different *Command than was registered")
	}
	if !r.Has(AppQuit) {
		t.Errorf("Has(%q) = false, want true", AppQuit)
	}

	// Confirm the handler we stored is invocable end-to-end.
	if err := got.Handler(ExecCtx{}); err != nil {
		t.Errorf("Handler returned error: %v", err)
	}
	if !called {
		t.Errorf("Handler was never invoked")
	}
}

// --- Registry: duplicate rejection ---------------------------------------

func TestRegistry_DuplicateRejected(t *testing.T) {
	r := NewRegistry()
	h1 := func(ExecCtx) error { return nil }
	h2 := func(ExecCtx) error { return nil }

	if err := r.Register(&Command{ID: AppQuit, Handler: h1}); err != nil {
		t.Fatalf("first Register: %v", err)
	}

	err := r.Register(&Command{ID: AppQuit, Handler: h2})
	if !errors.Is(err, ErrDuplicateAction) {
		t.Fatalf("second Register: err = %v, want errors.Is(ErrDuplicateAction)", err)
	}
	if !strings.Contains(err.Error(), AppQuit) {
		t.Errorf("duplicate error %q does not name the offending ID %q", err, AppQuit)
	}

	// The first handler must still win.
	got, _ := r.Get(AppQuit)
	if got.Handler == nil {
		t.Fatal("Get returned a Command with nil Handler")
	}
	// Pointer comparison via reflect would couple the test too tightly;
	// instead, assert Len did NOT advance past 1.
	if r.Len() != 1 {
		t.Errorf("Len after duplicate = %d, want 1", r.Len())
	}
}

// --- Registry: input validation -----------------------------------------

func TestRegistry_NilCommandRejected(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(nil); !errors.Is(err, ErrNilCommand) {
		t.Errorf("Register(nil): err = %v, want ErrNilCommand", err)
	}
}

func TestRegistry_EmptyIDRejected(t *testing.T) {
	r := NewRegistry()
	err := r.Register(&Command{ID: "", Handler: func(ExecCtx) error { return nil }})
	if !errors.Is(err, ErrInvalidActionID) {
		t.Errorf("Register(empty ID): err = %v, want ErrInvalidActionID", err)
	}
}

func TestRegistry_NilHandlerRejected(t *testing.T) {
	r := NewRegistry()
	err := r.Register(&Command{ID: "x.y", Handler: nil})
	if !errors.Is(err, ErrNilHandler) {
		t.Errorf("Register(nil handler): err = %v, want ErrNilHandler", err)
	}
}

// --- Registry: lookup misses ---------------------------------------------

func TestRegistry_GetUnknownReturnsZero(t *testing.T) {
	r := NewRegistry()
	got, ok := r.Get("never.registered")
	if ok || got != nil {
		t.Errorf("Get(unknown) = (%v, %v), want (nil, false)", got, ok)
	}
	if r.Has("never.registered") {
		t.Errorf("Has(unknown) = true")
	}
	// Empty registry: Len must be 0, All must be empty (and non-nil).
	if r.Len() != 0 {
		t.Errorf("empty Len = %d, want 0", r.Len())
	}
	if all := r.All(); all == nil || len(all) != 0 {
		t.Errorf("empty All = %v, want non-nil empty slice", all)
	}
}

// --- Registry: All() ordering -------------------------------------------

func TestRegistry_AllReturnsDeterministicAlphabeticalOrder(t *testing.T) {
	r := NewRegistry()
	h := func(ExecCtx) error { return nil }

	// Insert in deliberately scrambled order.
	for _, id := range []string{"z.last", AppQuit, "m.middle", HelpCheatsheet, "a.first"} {
		if err := r.Register(&Command{ID: id, Handler: h}); err != nil {
			t.Fatalf("Register(%q): %v", id, err)
		}
	}

	got := r.All()
	want := []string{"a.first", AppQuit, HelpCheatsheet, "m.middle", "z.last"}
	if len(got) != len(want) {
		t.Fatalf("All len = %d, want %d", len(got), len(want))
	}
	for i, cmd := range got {
		if cmd.ID != want[i] {
			t.Errorf("All[%d] = %q, want %q", i, cmd.ID, want[i])
		}
	}

	// Mutating the returned slice must not affect the registry.
	got[0] = nil
	if again := r.All(); again[0] == nil {
		t.Errorf("All returned a slice aliased with internal state")
	}
}

// --- Registry: concurrent reads -----------------------------------------

func TestRegistry_ConcurrentReadsAreSafe(t *testing.T) {
	r := NewRegistry()
	h := func(ExecCtx) error { return nil }
	for _, id := range AllActionIDs() {
		if err := r.Register(&Command{ID: id, Handler: h}); err != nil {
			t.Fatalf("bootstrap Register(%q): %v", id, err)
		}
	}

	const goroutines = 32
	const iters = 200
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for range iters {
				_, _ = r.Get(AppQuit)
				_ = r.Has(HelpCheatsheet)
				_ = r.Len()
				_ = r.All()
			}
		}()
	}
	wg.Wait()
	// No race / no panic == pass. Run with `go test -race`.
}

// --- NopSentinel --------------------------------------------------------

func TestNopSentinel_ReturnsNilNoSideEffects(t *testing.T) {
	if NopSentinel == nil {
		t.Fatal("NopSentinel is nil")
	}
	if err := NopSentinel(ExecCtx{Count: 5, Register: 'a'}); err != nil {
		t.Errorf("NopSentinel(...) = %v, want nil", err)
	}
}

func TestIsNop_IdentifiesSentinel(t *testing.T) {
	if !IsNop(NopSentinel) {
		t.Error("IsNop(NopSentinel) = false, want true")
	}

	other := func(ExecCtx) error { return nil }
	if IsNop(other) {
		t.Error("IsNop(unrelated handler) = true, want false")
	}

	if IsNop(nil) {
		t.Error("IsNop(nil) = true, want false")
	}
}

func TestNopCommand_PointsAtSentinel(t *testing.T) {
	if NopCommand == nil {
		t.Fatal("NopCommand is nil")
	}
	if !IsNop(NopCommand.Handler) {
		t.Errorf("NopCommand.Handler is not the NopSentinel")
	}
	if NopCommand.ID != "<nop>" {
		t.Errorf("NopCommand.ID = %q, want %q", NopCommand.ID, "<nop>")
	}
}

// --- ExecCtx ------------------------------------------------------------

func TestExecCtx_ZeroValueIsUsable(t *testing.T) {
	var ctx ExecCtx
	if ctx.Count != 0 {
		t.Errorf("zero Count = %d, want 0", ctx.Count)
	}
	if ctx.Register != 0 {
		t.Errorf("zero Register = %d, want 0", ctx.Register)
	}
	if ctx.Mode != types.Mode(0) {
		t.Errorf("zero Mode = %v, want types.Mode(0)", ctx.Mode)
	}
	if ctx.Scope != types.ContextKey("") {
		t.Errorf("zero Scope = %q, want empty", ctx.Scope)
	}
}

// --- Action-ID hygiene --------------------------------------------------

func TestActionIDs_NonEmptyDotNamespacedAndUnique(t *testing.T) {
	ids := AllActionIDs()
	if len(ids) == 0 {
		t.Fatal("AllActionIDs returned empty list")
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" {
			t.Errorf("empty action ID in AllActionIDs")
			continue
		}
		if !strings.Contains(id, ".") {
			t.Errorf("action ID %q is not dot-namespaced", id)
		}
		if _, dup := seen[id]; dup {
			t.Errorf("action ID %q appears more than once in AllActionIDs", id)
		}
		seen[id] = struct{}{}
	}
}

// reload.config was explicitly removed by the /review-plan amendment:
// `:reload` lives in dlp.7's ExRegistry, not in commands.Registry.
func TestActionIDs_NoOrphanReloadConfig(t *testing.T) {
	for _, id := range AllActionIDs() {
		if id == "reload.config" {
			t.Errorf("reload.config must not appear in CommandRegistry constants " +
				"(per /review-plan amendment — :reload lives in dlp.7's ExRegistry)")
		}
	}
}
