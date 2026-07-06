package theme

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/theme/builtin"
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

// TestApply_Prompt pins the Prompt wiring: Apply
// must parse cfg.Prompt into the themeState's Prompt field.
func TestApply_Prompt(t *testing.T) {
	cfg := builtin.DefaultDark()
	cfg.Prompt = "yellow"
	if err := Apply(cfg); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := Current()
	if got == nil {
		t.Fatal("Current returned nil")
	}
	if got.Prompt == nil {
		t.Fatal("Prompt is nil; Apply did not populate the field")
	}
	if got.Prompt.Fg != "yellow" {
		t.Errorf("Prompt.Fg = %q, want %q", got.Prompt.Fg, "yellow")
	}
}

// TestApply_PromptDefault asserts the built-in dark theme supplies a
// non-empty Prompt so RunLayout's COMMAND_LINE overlay always has a
// brightenable colour to use when no user override is configured.
func TestApply_PromptDefault(t *testing.T) {
	if err := Apply(builtin.DefaultDark()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := Current()
	if got == nil || got.Prompt == nil {
		t.Fatal("default-dark Prompt is nil")
	}
	if got.Prompt.Fg == "" {
		t.Fatal("default-dark Prompt.Fg is empty; expected a colour name")
	}
}

// TestIsMonochrome_Cached confirms IsMonochrome reads NO_COLOR once
// then returns the cached value on subsequent calls. Subsequent
// environment mutations are NOT honoured — the cache is process-
// lifetime.
func TestIsMonochrome_Cached(t *testing.T) {
	// First call resolves the value; we cannot guarantee what it is
	// (depends on whether NO_COLOR is set in the test runner's env).
	// What we CAN assert is that two consecutive calls return the
	// same boolean — the sync.Once cache invariant.
	first := IsMonochrome()
	second := IsMonochrome()
	if first != second {
		t.Fatalf("IsMonochrome returned %v then %v; cache must be stable", first, second)
	}
	// Mutating NO_COLOR after the cache resolves should NOT change
	// the return value.
	t.Setenv("NO_COLOR", "1")
	if IsMonochrome() != first {
		t.Fatalf("IsMonochrome flipped after NO_COLOR mutation; cache invariant broken")
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

// TestApply_MapsEveryThemeConfigField is the ThemeConfig<->themeState drift
// guard: it sets every exported string field of config.ThemeConfig to a valid
// sentinel ("white"), applies it, then walks the resulting themeState and
// asserts no *Style is nil and each carries the sentinel as its Fg. This fails
// if a new ThemeConfig field is added without a matching parseStyle(cfg.X) line
// in Apply (theme.go) — such a field would leave its themeState slot at the nil
// zero value.
func TestApply_MapsEveryThemeConfigField(t *testing.T) {
	const sentinel = "white"

	// Apply mutates the process-global theme; restore the default afterwards so
	// this test does not leak the sentinel state to later tests in the package.
	t.Cleanup(func() { _ = Apply(builtin.DefaultDark()) })

	var cfg config.ThemeConfig
	cv := reflect.ValueOf(&cfg).Elem()
	for i := 0; i < cv.NumField(); i++ {
		if cv.Type().Field(i).IsExported() && cv.Field(i).Kind() == reflect.String {
			cv.Field(i).SetString(sentinel)
		}
	}

	if err := Apply(&cfg); err != nil {
		t.Fatalf("Apply returned err: %v", err)
	}

	sv := reflect.ValueOf(*Current())
	tp := sv.Type()
	for i := 0; i < sv.NumField(); i++ {
		f := tp.Field(i)
		if !f.IsExported() || sv.Field(i).Kind() != reflect.Pointer {
			continue
		}
		fv := sv.Field(i)
		if fv.IsNil() {
			t.Errorf("themeState.%s is nil after Apply; missing parseStyle(cfg.%s) line in Apply", f.Name, f.Name)
			continue
		}
		if f.Name == "DirtyCell" {
			if got := fv.Interface().(*Style).Bg; got != sentinel {
				t.Errorf("themeState.%s.Bg = %q, want sentinel %q (bg-only field not wired from its ThemeConfig counterpart)", f.Name, got, sentinel)
			}
			continue
		}
		if got := fv.Interface().(*Style).Fg; got != sentinel {
			t.Errorf("themeState.%s.Fg = %q, want sentinel %q (field not wired from its ThemeConfig counterpart)", f.Name, got, sentinel)
		}
	}
}
