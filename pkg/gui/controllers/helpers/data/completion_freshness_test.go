package data

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/dbsavvy/pkg/gui/editor"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// editorBuf builds an editor.Buffer holding a single line — the minimal buffer a
// SchemaSource needs to detect a completion context. Only exported fields are
// set; the buffer's zero-value mutex is valid.
func editorBuf(line string) (*editor.Buffer, editor.Position) {
	b := &editor.Buffer{Lines: []editor.Line{{Runes: []rune(line)}}}
	return b, editor.Position{Line: 0, Col: len([]rune(line))}
}

// timeoutAfter returns a channel that fires after a generous bound used to fail a
// blocked Suggest call.
func timeoutAfter() <-chan time.Time { return time.After(2 * time.Second) }

// captureHandler is a thread-safe slog.Handler that records every emitted
// record, so a -race test can assert on logs.Event output produced from a worker
// goroutine without a data race on the buffer. Mirrors the orchestrator's
// adapters_test.go captureHandler.
type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

// hasEvent reports whether any captured record carries cat==cat && evt==evt and,
// when wantErr is non-empty, an "err" attr containing wantErr.
func (h *captureHandler) hasEvent(cat, evt, wantErr string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		var gotCat, gotEvt, gotErr string
		r.Attrs(func(a slog.Attr) bool {
			switch a.Key {
			case "cat":
				gotCat = a.Value.String()
			case "evt":
				gotEvt = a.Value.String()
			case "err":
				gotErr = a.Value.String()
			}
			return true
		})
		if gotCat == cat && gotEvt == evt {
			if wantErr == "" || strings.Contains(gotErr, wantErr) {
				return true
			}
		}
	}
	return false
}

// metaAdapter exposes a SchemaMetadataStore through the editor.SchemaMetadata
// interface so a SchemaSource can read the SAME store the warmer writes — the
// production wiring. This is what binds T3 (synchronous reads + reactive warm)
// to T4 (invalidation) in the freshness test below.
type metaAdapter struct{ s *SchemaMetadataStore }

func (m metaAdapter) TableNames(schema string) []string { return m.s.TableNames(schema) }
func (m metaAdapter) TableKind(schema, name string) string {
	return m.s.TableKind(schema, name)
}

func (m metaAdapter) Columns(schema, table string) ([]models.Column, bool) {
	return m.s.Columns(schema, table)
}

func (m metaAdapter) ForeignKeys(schema, table string) ([]models.ForeignKey, bool) {
	return m.s.ForeignKeys(schema, table)
}
func (m metaAdapter) FunctionNames() []string { return m.s.FunctionNames() }

// TestCompletionFreshness_AlterAddColumn_AutocompletesWithoutManualR proves the
// end-to-end freshness contract: after a local ALTER TABLE ADD COLUMN,
// the new column autocompletes with ZERO manual 'r' presses. It wires the REAL
// store + warmer + a real SchemaSource reading the store, with a fakeQuerier
// whose column set CHANGES after the simulated ALTER (mirroring a DDL applied to
// the live DB).
//
// Sequence (no manual refresh anywhere):
//  1. Warm "public.users" -> store holds the PRE-ALTER columns. Completion offers
//     them.
//  2. Local ALTER adds "email": the post-run DDL path calls
//     warmer.InvalidateSchema("public") — exactly what the controller
//     does after a successful DDL, no 'r'.
//  3. The NEXT completion trigger reads the store, sees the column entry UNLOADED
//     (invalidated), fires a reactive warm (re-trigger on miss), which
//     reloads the now-fresh column set including "email".
//  4. Completion now offers "email" — without any manual refresh keystroke.
func TestCompletionFreshness_AlterAddColumn_AutocompletesWithoutManualR(t *testing.T) {
	preAlter := []models.Column{{Name: "id"}, {Name: "name"}}
	postAlter := []models.Column{{Name: "id"}, {Name: "name"}, {Name: "email"}}

	f := &fakeQuerier{columns: preAlter, fks: nil}
	store := NewSchemaMetadataStore()
	store.SetTables("public", []TableEntry{{Name: "users", Kind: "table"}})

	// Synchronous deps: the warm body + UI publish run inline, so each completion
	// trigger is fully resolved by the time Suggest returns in this test — exactly
	// the determinism the re-trigger bridge gives the user across two frames.
	w := NewSchemaWarmer(store, syncDeps(f), nil)

	src := editor.NewSchemaSource(metaAdapter{store}, w, func() string { return "public" })

	complete := func(line string) []string {
		b, pos := editorBuf(line)
		sugs := src.Suggest(context.Background(), b, pos)
		out := make([]string, 0, len(sugs))
		for _, s := range sugs {
			out = append(out, s.Text)
		}
		return out
	}

	// Frame 1: column miss fires a reactive warm (synchronous here), which lands
	// the PRE-ALTER columns. The user's NEXT keystroke (Frame 2) re-triggers and
	// sees them — here we just trigger again to read the warmed result.
	_ = complete("SELECT users.")
	got := complete("SELECT users.")
	require.ElementsMatch(t, []string{"id", "name"}, got, "pre-ALTER columns autocomplete")
	require.NotContains(t, got, "email")

	// ---- Local ALTER TABLE users ADD COLUMN email ... ----
	// The driver now returns the post-ALTER column set on the next load.
	f.mu.Lock()
	f.columns = postAlter
	f.mu.Unlock()
	// Post-run DDL invalidation — the controller's success-gated path,
	// NOT a manual 'r'.
	w.InvalidateSchema("public")

	// Frame after the ALTER: trigger fires the reactive re-warm (store entry is
	// UNLOADED after invalidation), the warm reloads the fresh column set; the
	// re-trigger frame then reads it.
	_ = complete("SELECT users.")
	gotFresh := complete("SELECT users.")
	require.Contains(t, gotFresh, "email",
		"new column autocompletes after ALTER without any manual 'r' (invalidate + re-trigger)")
	require.ElementsMatch(t, []string{"id", "name", "email"}, gotFresh)
}

// TestWarmError_DriverFailureLoggedViaEvent proves jxw success criterion #4: a
// driver error during a warm is recorded via logs.Event (NOT silently dropped).
// It captures slog output through a test handler and asserts the warm-failure
// record is present with the underlying error attached.
func TestWarmError_DriverFailureLoggedViaEvent(t *testing.T) {
	hook := &captureHandler{}
	log := slog.New(hook)

	f := &fakeQuerier{colsErr: errors.New("permission denied for relation users")}
	w := NewSchemaWarmer(NewSchemaMetadataStore(), syncDeps(f), log)

	w.WarmTable("public", "users")

	require.True(t,
		hook.hasEvent("completion", "warm_columns_err", "permission denied for relation users"),
		"a warm-path driver error must be emitted via logs.Event, not swallowed")

	// And the entry stays UNLOADED (retryable) — the error did not poison the cache.
	_, ok := w.Store().Columns("public", "users")
	require.False(t, ok, "failed warm leaves the entry unloaded/retryable")
}

// TestWarmError_EagerFailureLoggedViaEvent proves the auth/permission eager-load
// path (LoadEager) likewise logs via logs.Event and surfaces as empty suggestions
// (not a crash) — jxw criterion #4's "auth failure on eager load is logged"
// edge path.
func TestWarmError_EagerFailureLoggedViaEvent(t *testing.T) {
	hook := &captureHandler{}
	log := slog.New(hook)

	f := &fakeQuerier{tablesErr: errors.New("permission denied for schema secure")}
	w := NewSchemaWarmer(NewSchemaMetadataStore(), syncDeps(f), log)

	w.LoadEager("secure")

	require.True(t,
		hook.hasEvent("completion", "eager_tables_err", "permission denied for schema secure"),
		"an eager-load auth failure must be logged via logs.Event")
	require.Nil(t, w.Store().TableNames("secure"),
		"failed eager load -> empty/unloaded suggestions, not a crash")
}

// TestCompletionFreshness_SuggestNonBlockingDuringInflightWarm proves jxw
// criterion #3 + the non-functional AC: the completion Suggest call returns
// WITHOUT waiting on the (slow) driver, even when a warm is in flight. The
// fakeQuerier's column load blocks on a gate; the deps defer the warm body onto a
// channel (never run during Suggest). Suggest must return immediately with the
// currently-cached (empty) result — proving it does not block on the network
// round-trip the way the jxw direct-session path did.
func TestCompletionFreshness_SuggestNonBlockingDuringInflightWarm(t *testing.T) {
	gate := make(chan struct{})
	f := &blockingQuerier{gate: gate}
	store := NewSchemaMetadataStore()
	store.SetTables("public", []TableEntry{{Name: "users", Kind: "table"}})

	// Defer the warm body so it is "in flight" (claimed) but never runs during the
	// Suggest call — the warm's driver load is the thing that blocks.
	workCh := make(chan func(gocui.Task) error, 4)
	deps := warmDeps{
		LoadTables:            f.LoadTables,
		LoadColumns:           f.LoadColumns,
		LoadForeignKeys:       f.LoadForeignKeys,
		LoadFunctions:         f.LoadFunctions,
		OnWorker:              func(fn func(gocui.Task) error) { workCh <- fn },
		OnUIThreadContentOnly: func(fn func() error) { _ = fn() },
	}
	w := NewSchemaWarmer(store, deps, nil)
	src := editor.NewSchemaSource(metaAdapter{store}, w, func() string { return "public" })

	b, pos := editorBuf("SELECT users.")

	// Suggest must return promptly even though the warm load would block on `gate`.
	done := make(chan []editor.Suggestion, 1)
	go func() { done <- src.Suggest(context.Background(), b, pos) }()

	select {
	case sugs := <-done:
		// Empty (column not yet warmed) but NON-blocking: the contract is that the
		// first uncached trigger returns immediately, not that it has data.
		require.Empty(t, sugs, "uncached trigger returns immediately with empty (warm fires async)")
	case <-timeoutAfter():
		t.Fatal("Suggest blocked on the in-flight warm's driver round-trip — jxw UI-block regression")
	}

	// The warm WAS dispatched (reactive warm fired) but its body is still parked.
	require.Len(t, workCh, 1, "the column miss fired exactly one reactive warm")

	// Drain so the goroutine that would run the load can be released cleanly; we
	// never run it (gate stays closed-to-unblock below) — just close the gate so a
	// hypothetical run would not deadlock test teardown.
	close(gate)
}

// blockingQuerier blocks LoadColumns on a gate to model a slow/remote DB. Used to
// prove Suggest never waits on the driver.
type blockingQuerier struct {
	gate chan struct{}
}

func (b *blockingQuerier) LoadTables(context.Context, string) ([]*models.Table, error) {
	return nil, nil
}

func (b *blockingQuerier) LoadColumns(_ context.Context, _, _ string) ([]models.Column, error) {
	<-b.gate
	return []models.Column{{Name: "id"}}, nil
}

func (b *blockingQuerier) LoadForeignKeys(context.Context, string, string) ([]models.ForeignKey, error) {
	return nil, nil
}
func (b *blockingQuerier) LoadFunctions(context.Context) ([]string, error) { return nil, nil }
