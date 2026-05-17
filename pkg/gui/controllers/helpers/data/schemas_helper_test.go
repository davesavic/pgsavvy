package data

import (
	"errors"
	"go/parser"
	"go/token"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/dbsavvy/pkg/common"
	guictx "github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// --- fake clock shared with app_state_store_test.go style ------------------

// fakeTimer is a Timer whose firing is controlled by the fake clock's Advance.
type fakeTimer struct {
	clk      *fakeClock
	deadline time.Time
	fn       func()
	stopped  bool
	fired    bool
}

func (t *fakeTimer) Stop() bool {
	t.clk.mu.Lock()
	defer t.clk.mu.Unlock()
	if t.fired || t.stopped {
		return false
	}
	t.stopped = true
	return true
}

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(1_700_000_000, 0)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, fn func()) common.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{
		clk:      c,
		deadline: c.now.Add(d),
		fn:       fn,
	}
	c.timers = append(c.timers, t)
	return t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	due := []*fakeTimer{}
	for _, t := range c.timers {
		if t.stopped || t.fired {
			continue
		}
		if !t.deadline.After(c.now) {
			t.fired = true
			due = append(due, t)
		}
	}
	c.mu.Unlock()
	for _, t := range due {
		t.fn()
	}
}

// newTestStore builds an AppStateStore on an in-memory FS with a controllable
// fake clock. The returned advance() flushes any pending debounced save.
func newTestStore(t *testing.T) (*common.AppStateStore, *fakeClock, func()) {
	t.Helper()
	fs := afero.NewMemMapFs()
	clk := newFakeClock()
	s := common.NewAppStateStore(fs, "/state.yml", clk)
	flush := func() {
		clk.Advance(common.DebounceWindow + time.Millisecond)
		require.NoError(t, s.Flush())
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, clk, flush
}

// --- FilterHidden ----------------------------------------------------------

func TestFilterHidden_Empty(t *testing.T) {
	h := NewSchemasHelper(common.NewDummyCommon(), nil)
	vis, hid := h.FilterHidden(nil, []string{"pg_*"}, nil, nil)
	require.Empty(t, vis)
	require.Empty(t, hid)
}

func TestFilterHidden_TableDriven(t *testing.T) {
	type tc struct {
		name        string
		raw         []models.Schema
		builtin     []string
		profile     []string
		runtime     []string
		wantVisible []string
		wantHidden  []string
	}
	cases := []tc{
		{
			name: "pg_temp glob matches numbered temp",
			raw: []models.Schema{
				{Name: "public"},
				{Name: "pg_temp_123"},
				{Name: "audit"},
			},
			builtin:     []string{"pg_temp_*"},
			wantVisible: []string{"public", "audit"},
			wantHidden:  []string{"pg_temp_123"},
		},
		{
			name: "dedupe across builtin profile and runtime overlap",
			raw: []models.Schema{
				{Name: "pg_catalog"},
				{Name: "public"},
			},
			builtin:     []string{"pg_catalog", "pg_*"},
			profile:     []string{"pg_catalog"},
			runtime:     []string{"pg_catalog"},
			wantVisible: []string{"public"},
			wantHidden:  []string{"pg_catalog"},
		},
		{
			name: "preserves raw input order in both outputs",
			raw: []models.Schema{
				{Name: "z_hidden"},
				{Name: "a_visible"},
				{Name: "m_hidden"},
				{Name: "b_visible"},
			},
			runtime:     []string{"z_hidden", "m_hidden"},
			wantVisible: []string{"a_visible", "b_visible"},
			wantHidden:  []string{"z_hidden", "m_hidden"},
		},
		{
			name: "name only match ignores owner",
			raw: []models.Schema{
				{Name: "public", Owner: "pg_catalog"}, // Owner equals a hide pattern
				{Name: "pg_catalog", Owner: "alice"},
			},
			builtin:     []string{"pg_catalog"},
			wantVisible: []string{"public"},
			wantHidden:  []string{"pg_catalog"},
		},
		{
			name: "overlapping globs still single entry",
			raw: []models.Schema{
				{Name: "pg_catalog"},
			},
			builtin:     []string{"pg_*", "pg_catalog"},
			wantVisible: nil,
			wantHidden:  []string{"pg_catalog"},
		},
		{
			name: "malformed pattern is skipped, others still apply",
			raw: []models.Schema{
				{Name: "public"},
				{Name: "audit"},
			},
			builtin:     []string{"[", "audit"}, // "[" → ErrBadPattern, "audit" still matches
			wantVisible: []string{"public"},
			wantHidden:  []string{"audit"},
		},
		{
			name: "no hide rules at all → everything visible",
			raw: []models.Schema{
				{Name: "public"},
				{Name: "audit"},
			},
			wantVisible: []string{"public", "audit"},
			wantHidden:  nil,
		},
		{
			name: "epic scenario: profile + builtin overlap with runtime addition",
			raw: []models.Schema{
				{Name: "public"},
				{Name: "pg_catalog"},
				{Name: "audit"},
			},
			builtin:     []string{"pg_catalog"},
			profile:     []string{"pg_catalog"},
			runtime:     []string{"audit"},
			wantVisible: []string{"public"},
			wantHidden:  []string{"pg_catalog", "audit"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := NewSchemasHelper(common.NewDummyCommon(), nil)
			vis, hid := h.FilterHidden(c.raw, c.builtin, c.profile, c.runtime)
			require.Equal(t, c.wantVisible, names(vis), "visible names")
			require.Equal(t, c.wantHidden, names(hid), "hidden names")
		})
	}
}

func TestFilterHidden_MalformedPatternDoesNotPanic(t *testing.T) {
	// Edge case: ALL patterns are malformed. Result should be every schema
	// visible (nothing matches), no panic, no error.
	h := NewSchemasHelper(common.NewDummyCommon(), nil)
	raw := []models.Schema{{Name: "a"}, {Name: "b"}}
	vis, hid := h.FilterHidden(raw, []string{"[", "[^"}, []string{"["}, []string{"["})
	require.Equal(t, []string{"a", "b"}, names(vis))
	require.Empty(t, hid)
}

// --- HideSchema ------------------------------------------------------------

func TestHideSchema_PersistsViaMutateAndSave(t *testing.T) {
	store, _, flush := newTestStore(t)
	h := NewSchemasHelper(common.NewDummyCommon(), store)

	require.NoError(t, h.HideSchema("conn-1", "audit"))
	flush()

	got := store.HiddenSchemasSnapshot("conn-1")
	require.Equal(t, []string{"audit"}, got)
}

func TestHideSchema_Idempotent(t *testing.T) {
	store, _, flush := newTestStore(t)
	h := NewSchemasHelper(common.NewDummyCommon(), store)

	require.NoError(t, h.HideSchema("conn-1", "audit"))
	require.NoError(t, h.HideSchema("conn-1", "audit"))
	require.NoError(t, h.HideSchema("conn-1", "audit"))
	flush()

	got := store.HiddenSchemasSnapshot("conn-1")
	require.Equal(t, []string{"audit"}, got, "duplicate Hide calls collapse to one entry")
}

func TestHideSchema_MultipleNamesPreserveOrder(t *testing.T) {
	store, _, flush := newTestStore(t)
	h := NewSchemasHelper(common.NewDummyCommon(), store)

	require.NoError(t, h.HideSchema("conn-1", "audit"))
	require.NoError(t, h.HideSchema("conn-1", "history"))
	require.NoError(t, h.HideSchema("conn-1", "scratch"))
	flush()

	got := store.HiddenSchemasSnapshot("conn-1")
	require.Equal(t, []string{"audit", "history", "scratch"}, got)
}

func TestHideSchema_PerConnectionIsolation(t *testing.T) {
	store, _, flush := newTestStore(t)
	h := NewSchemasHelper(common.NewDummyCommon(), store)

	require.NoError(t, h.HideSchema("conn-A", "audit"))
	require.NoError(t, h.HideSchema("conn-B", "scratch"))
	flush()

	require.Equal(t, []string{"audit"}, store.HiddenSchemasSnapshot("conn-A"))
	require.Equal(t, []string{"scratch"}, store.HiddenSchemasSnapshot("conn-B"))
}

// --- UnhideSchema ----------------------------------------------------------

func TestUnhideSchema_BuiltinMatchReturnsErrNeedsConfirmation(t *testing.T) {
	store, _, _ := newTestStore(t)
	h := NewSchemasHelper(common.NewDummyCommon(), store)

	// Pre-seed AppState with the schema hidden via the runtime layer too, so
	// we can prove no mutation happened despite the entry being present.
	store.MutateAndSave(func(s *common.AppState) {
		s.HiddenSchemas = map[string][]string{"conn-1": {"pg_catalog"}}
	})

	err := h.UnhideSchema("conn-1", "pg_catalog", []string{"pg_catalog"}, nil)
	require.ErrorIs(t, err, ErrNeedsConfirmation)

	// State unchanged.
	got := store.HiddenSchemasSnapshot("conn-1")
	require.Equal(t, []string{"pg_catalog"}, got, "ErrNeedsConfirmation must NOT mutate")
}

func TestUnhideSchema_ProfileMatchReturnsErrNeedsConfirmation(t *testing.T) {
	store, _, _ := newTestStore(t)
	h := NewSchemasHelper(common.NewDummyCommon(), store)

	store.MutateAndSave(func(s *common.AppState) {
		s.HiddenSchemas = map[string][]string{"conn-1": {"audit"}}
	})

	err := h.UnhideSchema("conn-1", "audit", nil, []string{"audit"})
	require.ErrorIs(t, err, ErrNeedsConfirmation)

	got := store.HiddenSchemasSnapshot("conn-1")
	require.Equal(t, []string{"audit"}, got)
}

func TestUnhideSchema_ProfileGlobReturnsErrNeedsConfirmation(t *testing.T) {
	// Glob (not literal) match must also trigger confirmation per the predicate
	// using path.Match — same matchesAny path as FilterHidden.
	store, _, _ := newTestStore(t)
	h := NewSchemasHelper(common.NewDummyCommon(), store)

	err := h.UnhideSchema("conn-1", "pg_temp_42", []string{"pg_temp_*"}, nil)
	require.ErrorIs(t, err, ErrNeedsConfirmation)
}

func TestUnhideSchema_RuntimeOnlyRemovesEntry(t *testing.T) {
	store, _, flush := newTestStore(t)
	h := NewSchemasHelper(common.NewDummyCommon(), store)

	// Seed runtime hide for two schemas; neither matches builtin/profile.
	require.NoError(t, h.HideSchema("conn-1", "audit"))
	require.NoError(t, h.HideSchema("conn-1", "scratch"))
	flush()
	require.Equal(t, []string{"audit", "scratch"}, store.HiddenSchemasSnapshot("conn-1"))

	// Unhide one. builtin/profile lists deliberately empty → runtime-only path.
	require.NoError(t, h.UnhideSchema("conn-1", "audit", nil, nil))
	flush()
	require.Equal(t, []string{"scratch"}, store.HiddenSchemasSnapshot("conn-1"))
}

func TestUnhideSchema_NotPresentNoOpNil(t *testing.T) {
	store, _, flush := newTestStore(t)
	h := NewSchemasHelper(common.NewDummyCommon(), store)

	// Empty state, ask to unhide something that was never hidden.
	require.NoError(t, h.UnhideSchema("conn-1", "ghost", nil, nil))
	flush()
	require.Nil(t, store.HiddenSchemasSnapshot("conn-1"))
}

func TestUnhideSchema_LastEntryDropsKey(t *testing.T) {
	store, _, flush := newTestStore(t)
	h := NewSchemasHelper(common.NewDummyCommon(), store)

	require.NoError(t, h.HideSchema("conn-1", "audit"))
	flush()
	require.Equal(t, []string{"audit"}, store.HiddenSchemasSnapshot("conn-1"))

	require.NoError(t, h.UnhideSchema("conn-1", "audit", nil, nil))
	flush()
	// Snapshot returns nil when no entry exists for the key.
	require.Nil(t, store.HiddenSchemasSnapshot("conn-1"))
}

// --- ToggleShowHidden ------------------------------------------------------

// newSchemasContext builds a real *context.SchemasContext suitable for tests.
// Uses an empty deps bag — ToggleShowHidden does not touch the gui driver.
func newSchemasContext(t *testing.T) *guictx.SchemasContext {
	t.Helper()
	base := guictx.NewBaseContext(guictx.BaseContextOpts{
		Key:      types.SCHEMAS,
		ViewName: string(types.SCHEMAS),
		Kind:     types.SIDE_CONTEXT,
	})
	return guictx.NewSchemasContext(base, guictx.Deps{})
}

func TestToggleShowHidden_FlipsAcrossMultiplePresses(t *testing.T) {
	h := NewSchemasHelper(common.NewDummyCommon(), nil)
	ctx := newSchemasContext(t)

	require.False(t, ctx.GetShowHiddenMode(), "fresh context defaults to off")

	h.ToggleShowHidden(ctx)
	require.True(t, ctx.GetShowHiddenMode(), "1st toggle → on")

	h.ToggleShowHidden(ctx)
	require.False(t, ctx.GetShowHiddenMode(), "2nd toggle → off")

	h.ToggleShowHidden(ctx)
	require.True(t, ctx.GetShowHiddenMode(), "3rd toggle → on")
}

func TestToggleShowHidden_NilContextNoop(t *testing.T) {
	h := NewSchemasHelper(common.NewDummyCommon(), nil)
	// No panic on nil context — defensive guard for partial wiring.
	h.ToggleShowHidden(nil)
}

func TestToggleShowHidden_DoesNotPersist(t *testing.T) {
	// AC: ToggleShowHidden must not write to AppState. Run the toggle a few
	// times and confirm the store remained idle (no HiddenSchemas entries
	// created, no LastConnectionID etc. touched).
	store, _, _ := newTestStore(t)
	h := NewSchemasHelper(common.NewDummyCommon(), store)
	ctx := newSchemasContext(t)

	h.ToggleShowHidden(ctx)
	h.ToggleShowHidden(ctx)
	h.ToggleShowHidden(ctx)

	// We can't compare *AppState directly (it's owned by the store), but
	// HiddenSchemasSnapshot is a window into it: any toggle-driven write
	// would have to put data SOMEWHERE, and the only fields ToggleShowHidden
	// could plausibly touch are unobservable from outside. Best signal: the
	// store reports no save error AND no hidden-schema entry materialized.
	require.NoError(t, store.LastSaveErr())
	require.Nil(t, store.HiddenSchemasSnapshot("any-conn"))
}

// TestToggleShowHidden_SerialPressesAreRaceClean exercises the toggle from a
// single goroutine across many presses, simulating the production hot path
// (gocui main loop — bindings dispatch is single-threaded). This is the
// strongest race-safety guarantee the helper can offer WITHOUT modifying
// pkg/gui/context/schemas_context.go (T2-owned).
//
// Deviation note (surfaced in the implementation report): the original AC
// reads "100 concurrent toggles must be -race clean". With the current T2
// implementation (plain bool field, no mutex/atomic), a -race run with N
// concurrent toggles flags a data race on showHiddenMode. The spec told this
// task NOT to edit schemas_context.go and to surface the finding instead.
// This test therefore validates the production-realistic single-goroutine
// path; concurrent-safety must be added to SchemasContext by T2 (or hoisted
// back to it as an amendment) before the original AC can be honored.
func TestToggleShowHidden_SerialPressesAreRaceClean(t *testing.T) {
	h := NewSchemasHelper(common.NewDummyCommon(), nil)
	ctx := newSchemasContext(t)

	const N = 100
	for i := 0; i < N; i++ {
		h.ToggleShowHidden(ctx)
	}
	// Final parity: 100 toggles from "off" → off.
	require.False(t, ctx.GetShowHiddenMode(),
		"even count of toggles must return to the initial state under serial dispatch")
}

// --- static-shape asserts --------------------------------------------------

// TestHelperHasNoGocuiImport parses the helper source file with go/parser and
// asserts no gocui package appears in the import set. This is the AC check
// "Helper has zero gocui imports" implemented as a unit test rather than a
// grep step.
func TestHelperHasNoGocuiImport(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "schemas_helper.go", nil, parser.ImportsOnly)
	require.NoError(t, err)
	for _, imp := range f.Imports {
		require.NotContains(t, imp.Path.Value, "gocui",
			"schemas_helper.go must not import gocui (found %s)", imp.Path.Value)
	}
}

func TestErrNeedsConfirmationIsExported(t *testing.T) {
	// Sanity: the sentinel is non-nil and survives errors.Is wrapping.
	require.NotNil(t, ErrNeedsConfirmation)

	// Plain string concat must NOT satisfy Is — proves the sentinel isn't a
	// global "match anything" interface.
	plain := errors.New("wrap: " + ErrNeedsConfirmation.Error())
	require.NotErrorIs(t, plain, ErrNeedsConfirmation)

	// Proper Unwrap()-chained wrap MUST satisfy Is.
	wrapped := wrappedErr{inner: ErrNeedsConfirmation}
	require.ErrorIs(t, wrapped, ErrNeedsConfirmation)
}

// wrappedErr is a tiny error type that implements Unwrap so errors.Is works.
// Used in TestErrNeedsConfirmationIsExported instead of fmt.Errorf to keep
// the test file's import surface tight.
type wrappedErr struct{ inner error }

func (w wrappedErr) Error() string { return "wrapped: " + w.inner.Error() }
func (w wrappedErr) Unwrap() error { return w.inner }

// --- shared utilities -----------------------------------------------------

func names(in []models.Schema) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = s.Name
	}
	return out
}
