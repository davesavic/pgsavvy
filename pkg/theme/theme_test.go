package theme

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/theme/builtin"
)

func TestApply_DefaultDark_SetsActiveBorderToHighContrast(t *testing.T) {
	if err := Apply(builtin.DefaultDark()); err != nil {
		t.Fatalf("Apply returned err: %v", err)
	}
	got := Current()
	if got == nil {
		t.Fatal("Current returned nil after Apply")
	}
	if got.ActiveBorder == nil {
		t.Fatal("ActiveBorder is nil")
	}
	if got.ActiveBorder.Fg != "yellow" {
		t.Fatalf("ActiveBorder.Fg = %q, want %q", got.ActiveBorder.Fg, "yellow")
	}
}

func TestApply_Nil_ReturnsErrorAndPreservesState(t *testing.T) {
	// Establish a known prior state.
	if err := Apply(builtin.DefaultDark()); err != nil {
		t.Fatalf("seed Apply: %v", err)
	}
	prevFg := Current().ActiveBorder.Fg

	if err := Apply(nil); err == nil {
		t.Fatal("Apply(nil) returned nil err; expected non-nil")
	}

	got := Current()
	if got == nil {
		t.Fatal("Current returned nil after Apply(nil)")
	}
	if got.ActiveBorder == nil {
		t.Fatal("ActiveBorder nil after Apply(nil)")
	}
	if got.ActiveBorder.Fg != prevFg {
		t.Fatalf("ActiveBorder.Fg = %q, want %q (state should be preserved)",
			got.ActiveBorder.Fg, prevFg)
	}
}

func TestApply_UnknownColor_NoError(t *testing.T) {
	cfg := builtin.DefaultDark()
	cfg.ActiveBorder = "notacolor"
	if err := Apply(cfg); err != nil {
		t.Fatalf("Apply with unknown color returned err: %v", err)
	}
	got := Current()
	if got == nil {
		t.Fatal("Current returned nil")
	}
	if got.ActiveBorder == nil {
		t.Fatal("ActiveBorder is nil; parseStyle must always return non-nil")
	}
	if got.ActiveBorder.Fg != "notacolor" {
		t.Fatalf("ActiveBorder.Fg = %q, want %q", got.ActiveBorder.Fg, "notacolor")
	}

	// Restore for other tests.
	if err := Apply(builtin.DefaultDark()); err != nil {
		t.Fatalf("restore Apply: %v", err)
	}
}

func TestCurrent_NeverReturnsNil_AfterStoreNil(t *testing.T) {
	prev := current.Load()
	t.Cleanup(func() {
		current.Store(prev)
		initOnce = sync.Once{}
	})

	current.Store(nil)
	initOnce = sync.Once{}

	got := Current()
	if got == nil {
		t.Fatal("Current() returned nil after current.Store(nil); lazy-init invariant broken")
	}
}

func TestApply_Concurrent_ReadersAndWriter(t *testing.T) {
	// Seed a known state so readers always see a valid snapshot.
	if err := Apply(builtin.DefaultDark()); err != nil {
		t.Fatalf("seed Apply: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	const numReaders = 50
	var wg sync.WaitGroup

	for range numReaders {
		wg.Go(func() {
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				state := Current()
				if state == nil {
					t.Errorf("reader observed nil state")
					return
				}
				_ = state.ActiveBorder
			}
		})
	}

	wg.Go(func() {
		cfg := builtin.DefaultDark()
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			_ = Apply(cfg)
		}
	})

	// Stop quickly so -count=10 stays fast.
	time.AfterFunc(100*time.Millisecond, cancel)
	wg.Wait()

	// Final invariant check.
	if Current() == nil {
		t.Fatal("Current() nil after concurrent run")
	}
}

// TestApply_PromptFg pins the dbsavvy-tro.12 PromptFg wiring: Apply
// must parse cfg.PromptFg into the themeState's PromptFg field.
func TestApply_PromptFg(t *testing.T) {
	cfg := builtin.DefaultDark()
	cfg.PromptFg = "yellow"
	if err := Apply(cfg); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := Current()
	if got == nil {
		t.Fatal("Current returned nil")
	}
	if got.PromptFg == nil {
		t.Fatal("PromptFg is nil; Apply did not populate the field")
	}
	if got.PromptFg.Fg != "yellow" {
		t.Errorf("PromptFg.Fg = %q, want %q", got.PromptFg.Fg, "yellow")
	}
}

// TestApply_PromptFgDefault asserts the built-in dark theme supplies a
// non-empty PromptFg so RunLayout's COMMAND_LINE overlay always has a
// brightenable colour to use when no user override is configured.
func TestApply_PromptFgDefault(t *testing.T) {
	if err := Apply(builtin.DefaultDark()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := Current()
	if got == nil || got.PromptFg == nil {
		t.Fatal("default-dark PromptFg is nil")
	}
	if got.PromptFg.Fg == "" {
		t.Fatal("default-dark PromptFg.Fg is empty; expected a colour name")
	}
}

func TestParseStyle_AlwaysNonNil(t *testing.T) {
	cases := []string{"", "red", "#123456", "notacolor"}
	for _, c := range cases {
		s := parseStyle(c)
		if s == nil {
			t.Errorf("parseStyle(%q) returned nil", c)
			continue
		}
		if s.Fg != c {
			t.Errorf("parseStyle(%q).Fg = %q, want %q", c, s.Fg, c)
		}
	}
}
