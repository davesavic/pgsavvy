package data

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/logs"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// warmFailureCooldown is how long a (schema,table) warm whose driver call
// FAILED is suppressed from re-issuing before a retry is allowed.
//
// ASSUMPTION (review Finding G): the spec asks for "a few seconds"; 3s is a
// deliberate middle ground — long enough that a burst of completion keystrokes
// after a transient failure does not hammer the driver, short enough that a
// recovered connection re-warms within one user pause. The store entry itself
// stays logically UNLOADED (retryable) — the cooldown lives ONLY in the warmer,
// never as a cached store failure.
const warmFailureCooldown = 3 * time.Second

// warmDeps is the dependency seam the SchemaWarmer depends on. It is defined as
// a set of func fields (mirroring the OnWorker/OnUIThread plumbing convention in
// helpers/ui/result_tabs_helper.go) so tests inject fully synchronous,
// -race-clean fakes without standing up a live Session or the gocui MainLoop.
//
// ASSUMPTION (dependency-seam): ConnectHelper.submit is unexported and
// ConnectHelper exposes no LoadFunctions wrapper, so the warmer cannot call a
// serialized ListFunctions directly. Rather than widen connect_helper.go (a
// Tier-3 change), the seam takes LoadFunctions as an injected closure. Production
// wiring (T3) supplies one that routes ListFunctions through the SAME serialized
// path (e.g. a thin ConnectHelper.LoadFunctions wrapper). The warmer NEVER calls
// a raw drivers.Session method itself.
//
// Threading contract:
//   - LoadTables/LoadColumns/LoadForeignKeys/LoadFunctions are the serialized
//     ConnectHelper wrappers — each runs exactly one query through the per-Session
//     worker queue and may be called from any goroutine.
//   - OnWorker dispatches a closure onto a tracked background goroutine
//     (orchestrator.Gui.OnWorker).
//   - OnUIThreadContentOnly schedules a closure onto the gocui MainLoop with the
//     content-only fast path (orchestrator.Gui.OnUIThreadContentOnly).
type warmDeps struct {
	LoadTables      func(ctx context.Context, schema string) ([]*models.Table, error)
	LoadColumns     func(ctx context.Context, schema, table string) ([]models.Column, error)
	LoadForeignKeys func(ctx context.Context, schema, table string) ([]models.ForeignKey, error)
	LoadFunctions   func(ctx context.Context) ([]string, error)

	OnWorker              func(func(gocui.Task) error)
	OnUIThreadContentOnly func(func() error)
}

// warmKeyState tracks the per-(schema,table) lifecycle the WARMER owns (the
// store stays a pure cache). A key is present in the map iff it is in-flight,
// in a post-failure cooldown, or carries a non-zero generation; success without
// a superseding invalidation removes it so the store's loaded entry becomes the
// single source of truth.
type warmKeyState struct {
	inflight bool
	// cooldownUntil is the wall-clock instant before which a failed key must
	// not be re-warmed. Zero ⇔ no active cooldown.
	cooldownUntil time.Time
	// gen is the per-key generation/epoch. Any invalidation
	// (InvalidateTable/InvalidateSchema/Reset) that targets this key bumps it.
	// WarmTable captures gen at claim time and, before publishing on the UI
	// loop, re-checks it: a mismatch means an invalidation landed while the warm
	// was in flight, so the (now-stale) result is DROPPED rather than written
	// back into the store. This closes the invalidate-then-late-publish race
	// where a same-table warm already in flight would otherwise repopulate the
	// store with pre-ALTER columns.
	gen uint64
}

// SchemaWarmer owns a SchemaMetadataStore and populates it through the
// serialized ConnectHelper path (eager table+function names; lazy per-table
// columns+FKs). It is the write side of the two-tier metadata snapshot; T3 reads
// the store synchronously and wires SetOnWarmed to re-trigger completion.
//
// Concurrency: all exported methods are safe to call from any goroutine. The
// warmer's own state map is guarded by stateMu; the store has its own lock.
type SchemaWarmer struct {
	store *SchemaMetadataStore
	deps  warmDeps
	log   *slog.Logger

	// now is the injectable clock for the failure-cooldown machine; defaults to
	// time.Now so tests drive cooldown expiry deterministically.
	now func() time.Time

	mu    sync.Mutex
	state map[string]*warmKeyState

	// callbacks are read under mu so SetOnWarmed/SetOnWarmError race-cleanly
	// with in-flight warms.
	onWarmed    func(schema, table string)
	onWarmError func(schema, table string, err error)
}

// NewSchemaWarmer builds a warmer over store, wiring the serialized loaders and
// threading primitives via deps. A nil logger is tolerated (logs.Event is
// nil-safe). onWarmed/onWarmError default to no-ops until set.
func NewSchemaWarmer(store *SchemaMetadataStore, deps warmDeps, log *slog.Logger) *SchemaWarmer {
	return &SchemaWarmer{
		store:       store,
		deps:        deps,
		log:         log,
		now:         time.Now,
		state:       make(map[string]*warmKeyState),
		onWarmed:    func(string, string) {},
		onWarmError: func(string, string, error) {},
	}
}

// NewConnectSchemaWarmer is the production constructor: it builds a warmer over
// a fresh store, wiring the serialized loaders to ConnectHelper's LoadX
// wrappers and the threading primitives to the orchestrator's OnWorker /
// OnUIThreadContentOnly. It exists because warmDeps is unexported (the
// func-field seam stays an implementation detail), so the orchestrator (a
// different package) cannot assemble it directly. Returns nil if helper is nil.
func NewConnectSchemaWarmer(
	helper *ConnectHelper,
	onWorker func(func(gocui.Task) error),
	onUIThreadContentOnly func(func() error),
	log *slog.Logger,
) *SchemaWarmer {
	if helper == nil {
		return nil
	}
	deps := warmDeps{
		LoadTables:            helper.LoadTables,
		LoadColumns:           helper.LoadColumns,
		LoadForeignKeys:       helper.LoadForeignKeys,
		LoadFunctions:         helper.LoadFunctions,
		OnWorker:              onWorker,
		OnUIThreadContentOnly: onUIThreadContentOnly,
	}
	return NewSchemaWarmer(NewSchemaMetadataStore(), deps, log)
}

// Store returns the warmer's metadata store so consumers (T3) read it directly.
func (w *SchemaWarmer) Store() *SchemaMetadataStore { return w.store }

// Reset drops all warmed metadata AND the warmer's per-key cooldown/in-flight
// bookkeeping. Used on reconnect: without clearing the
// cooldown `state` map, a table that was in a post-failure cooldown window at
// disconnect would stay suppressed for up to warmFailureCooldown after the new
// connection lands, so its lazy entry would not re-warm. Resetting the store
// guarantees no entry from the prior connection survives; resetting state
// guarantees the next WarmTable for any key is allowed to issue a fresh load.
//
// In-flight warms from the prior connection are NOT awaited here (they run on
// their own worker and publish via OnUIThreadContentOnly). Replacing the state
// map swaps out every in-flight warm's claimed state pointer, so the generation
// guard in clearInflight DROPS their late publishes (the live key, if any, is a
// fresh struct, not the one they captured) rather than letting them repopulate a
// stale entry. The next claim proceeds against the fresh map.
func (w *SchemaWarmer) Reset() {
	w.store.Reset()
	w.mu.Lock()
	w.state = make(map[string]*warmKeyState)
	w.mu.Unlock()
}

// InvalidateSchema drops every lazy (column+FK) entry for schema in the store
// and clears any cooldown/in-flight bookkeeping for keys in that schema, so the
// next WarmTable for an affected table issues a fresh load. The thin wrapper
// exists so post-run / manual-refresh callers reach the store through the
// warmer (its single owner) without also having to poke the cooldown map.
func (w *SchemaWarmer) InvalidateSchema(schema string) {
	w.store.InvalidateSchema(schema)
	prefix := schema + "\x00"
	w.mu.Lock()
	for key, st := range w.state {
		if strings.HasPrefix(key, prefix) {
			w.supersedeLocked(key, st)
		}
	}
	w.mu.Unlock()
}

// InvalidateTable drops the lazy (column+FK) entry for (schema,table) in the
// store and clears its cooldown/in-flight bookkeeping, so the next WarmTable
// re-loads it. Used by the manual 'r' force-refresh path.
func (w *SchemaWarmer) InvalidateTable(schema, table string) {
	w.store.InvalidateTable(schema, table)
	key := tableKey(schema, table)
	w.mu.Lock()
	if st := w.state[key]; st != nil {
		w.supersedeLocked(key, st)
	}
	w.mu.Unlock()
}

// supersedeLocked records that an invalidation has superseded any work for key.
// Caller must hold w.mu. If the key has an in-flight warm, the entry is kept
// (with a bumped generation + cleared cooldown) so the returning warm sees the
// generation mismatch and DROPS its stale publish; the bumped generation is the
// signal. If there is no in-flight warm, the entry can simply be removed (the
// store entry is already dropped by the InvalidateX store call, and a fresh
// claim starts from a clean slate).
func (w *SchemaWarmer) supersedeLocked(key string, st *warmKeyState) {
	if !st.inflight {
		delete(w.state, key)
		return
	}
	st.gen++
	st.cooldownUntil = time.Time{}
}

// SetOnWarmed registers the callback fired exactly once on the UI loop after a
// (schema,table) warm completes successfully. A nil fn resets to a no-op.
func (w *SchemaWarmer) SetOnWarmed(fn func(schema, table string)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if fn == nil {
		fn = func(string, string) {}
	}
	w.onWarmed = fn
}

// SetOnWarmError registers an optional callback invoked (on the UI loop) when a
// warm's driver call fails, so the controller layer can surface a THROTTLED
// user-visible signal. The warmer deliberately does NOT import the ui helper
// layer (ToastHelper lives in helpers/ui; importing it from helpers/data would
// cross the helper layering), so toast wiring is the caller's job. A nil fn
// resets to a no-op. logs.Event still fires unconditionally on every failure.
func (w *SchemaWarmer) SetOnWarmError(fn func(schema, table string, err error)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if fn == nil {
		fn = func(string, string, error) {}
	}
	w.onWarmError = fn
}

// LoadEager loads the eager tier for a schema through the serialized path:
// table+view names FIRST (Finding O: tables must not wait on pg_proc), then the
// per-connection function names. Both run as one query each. A failure on either
// is logged via logs.Event and leaves that tier unloaded (a subsequent LoadEager
// retries); table-name failure does NOT abort the function-name load.
//
// LoadEager runs synchronously on the caller's goroutine: the ConnectHelper
// wrappers already serialize onto the worker queue, and connect/schema-select
// callers invoke it off the UI thread.
func (w *SchemaWarmer) LoadEager(schema string) {
	ctx := context.Background()

	// Tables FIRST (Finding O).
	tables, err := w.deps.LoadTables(ctx, schema)
	if err != nil {
		logs.Event(w.log, "completion", "eager_tables_err",
			slog.String("schema", schema), slog.String("err", err.Error()))
	} else {
		entries := tableEntries(tables)
		w.store.SetTables(schema, entries)
		logs.Event(w.log, "completion", "eager_tables",
			slog.String("schema", schema), slog.Int("count", len(entries)))
	}

	// Function names SECOND (per-connection, not per-schema).
	fns, err := w.deps.LoadFunctions(ctx)
	if err != nil {
		logs.Event(w.log, "completion", "eager_functions_err",
			slog.String("schema", schema), slog.String("err", err.Error()))
		return
	}
	w.store.SetFunctionNames(fns)
	logs.Event(w.log, "completion", "eager_functions",
		slog.Int("count", len(fns)))
}

// WarmTable lazily loads the columns + foreign keys for (schema,table) off the
// UI goroutine (OnWorker) and publishes them on the UI loop
// (OnUIThreadContentOnly). It is idempotent: a key already loaded in the store,
// currently in-flight, or inside its post-failure cooldown window is a no-op, so
// at most one driver round-trip is issued per key (until success or cooldown
// elapse). On success onWarmed fires exactly once on the UI loop; on failure the
// store entry stays UNLOADED (retryable after cooldown) and the error is logged
// + routed to onWarmError.
func (w *SchemaWarmer) WarmTable(schema, table string) {
	st, gen, ok := w.claim(schema, table)
	if !ok {
		return
	}

	w.deps.OnWorker(func(gocui.Task) error {
		w.runWarm(schema, table, st, gen)
		return nil
	})
}

// claim atomically decides whether a warm for (schema,table) should proceed and,
// if so, marks it in-flight. Returns ok==false (no-op) when the key is already
// loaded in the store, already in-flight, or still inside its failure cooldown.
// On success it also returns the claimed key state pointer and the generation
// captured at claim time; runWarm re-checks both before publishing so an
// invalidation that landed mid-flight (which bumps the generation, or swaps the
// state pointer on Reset) causes the stale result to be dropped.
func (w *SchemaWarmer) claim(schema, table string) (*warmKeyState, uint64, bool) {
	key := tableKey(schema, table)
	w.mu.Lock()
	defer w.mu.Unlock()

	// The store-loaded check MUST be sequenced under w.mu with the in-flight /
	// delete bookkeeping, otherwise a TOCTOU lets two concurrent callers for the
	// same key both observe "not loaded, not in-flight" and both issue a load.
	// runWarm's success path writes the store entry (SetColumns) BEFORE removing
	// the key from w.state under w.mu, so any claimer that acquires w.mu after the
	// winning warm's clearInflight() sees the store loaded here; any claimer that
	// acquires it before sees inflight==true below. Together they guarantee
	// exactly one round-trip per key.
	if _, loaded := w.store.Columns(schema, table); loaded {
		return nil, 0, false
	}

	st := w.state[key]
	if st == nil {
		st = &warmKeyState{}
		w.state[key] = st
	}
	if st.inflight {
		return nil, 0, false
	}
	if !st.cooldownUntil.IsZero() && w.now().Before(st.cooldownUntil) {
		return nil, 0, false
	}
	st.inflight = true
	st.cooldownUntil = time.Time{}
	return st, st.gen, true
}

// runWarm executes the two serialized driver loads on the worker goroutine and
// schedules the store publish + callbacks on the UI loop. It is the body the
// OnWorker closure runs; claim has already marked the key in-flight and handed
// back the key-state pointer + generation captured at claim time. The publish
// is gated on that generation still being live (see clearInflight): an
// invalidation that landed mid-flight drops the stale result.
func (w *SchemaWarmer) runWarm(schema, table string, st *warmKeyState, gen uint64) {
	ctx := context.Background()

	cols, err := w.deps.LoadColumns(ctx, schema, table)
	if err != nil {
		w.failWarm(schema, table, st, gen, "warm_columns_err", err)
		return
	}
	fks, err := w.deps.LoadForeignKeys(ctx, schema, table)
	if err != nil {
		w.failWarm(schema, table, st, gen, "warm_fks_err", err)
		return
	}

	w.deps.OnUIThreadContentOnly(func() error {
		// Generation guard + atomic publish: clearInflight re-checks (under w.mu)
		// that this warm is still live, and on the live path runs the store write
		// BEFORE removing the key from w.state — all in one w.mu hold. Sequencing
		// SetColumns ahead of the delete under the same lock that claim() takes is
		// what makes the success transition atomic w.r.t. concurrent claimers:
		// a claimer that wins the lock before the delete sees inflight==true; one
		// that wins after the delete sees the store already loaded. Without that
		// ordering two callers can both observe "not loaded, not in-flight" and
		// each issue a round-trip.
		//
		// If an invalidation superseded this warm while it was in flight, the
		// publish is DROPPED (store left UNLOADED, onWarmed not fired) and the key
		// is left re-warmable.
		publish := func() {
			logs.Event(w.log, "completion", "warm_table",
				slog.String("schema", schema), slog.String("table", table),
				slog.Int("columns", len(cols)), slog.Int("fks", len(fks)))
			w.store.SetColumns(schema, table, cols)
			w.store.SetForeignKeys(schema, table, fks)
		}
		if !w.clearInflight(schema, table, st, gen, false, publish) {
			logs.Event(w.log, "completion", "warm_table_superseded",
				slog.String("schema", schema), slog.String("table", table))
			return nil
		}
		w.snapshotOnWarmed()(schema, table)
		return nil
	})
}

// failWarm logs the failure, opens a cooldown window for the key, and routes the
// error to onWarmError on the UI loop. The store entry is left UNLOADED so a
// later retry (after cooldown) re-issues the load. If an invalidation superseded
// the warm mid-flight, no cooldown is opened and onWarmError is suppressed (the
// key is simply made re-warmable), so a stale failure cannot leak past a fresh
// invalidation.
func (w *SchemaWarmer) failWarm(schema, table string, st *warmKeyState, gen uint64, evt string, err error) {
	logs.Event(w.log, "completion", evt,
		slog.String("schema", schema), slog.String("table", table),
		slog.String("err", err.Error()))

	if !w.clearInflight(schema, table, st, gen, true, nil) {
		return
	}

	w.deps.OnUIThreadContentOnly(func() error {
		w.snapshotOnWarmError()(schema, table, err)
		return nil
	})
}

// clearInflight finalises an in-flight warm. It returns false (a "dropped"
// signal) when the warm was superseded by an invalidation since claim — either
// the key's live state was swapped out (Reset / no longer present, e.g. an
// InvalidateX removed a non-inflight-looking entry) or its generation advanced
// (InvalidateTable/InvalidateSchema bumped gen). In the superseded case the live
// key (if any) is left untouched and re-warmable.
//
// When the warm is still live: on success (failed=false) publish (if non-nil) is
// run under w.mu and then the key is removed so the store becomes the single
// source of truth — atomically w.r.t. claim(). On failure a cooldown window is
// opened so the failed key is suppressed until it elapses.
func (w *SchemaWarmer) clearInflight(schema, table string, st *warmKeyState, gen uint64, failed bool, publish func()) bool {
	key := tableKey(schema, table)
	w.mu.Lock()
	defer w.mu.Unlock()

	live := w.state[key]
	if live != st || live.gen != gen {
		// Superseded: a fresh state pointer (Reset) or a bumped generation
		// (Invalidate*) means our result is stale. Clear our own stale claim's
		// in-flight flag (it is no longer the live entry, but defensively reset
		// it so the orphaned struct is inert) and leave the live key re-warmable.
		st.inflight = false
		return false
	}

	st.inflight = false
	if !failed {
		// Write the store entry BEFORE deleting the key, both under w.mu, so a
		// concurrent claim() either sees inflight==true (acquires before us) or the
		// loaded store entry (acquires after us) — never an empty slate.
		if publish != nil {
			publish()
		}
		delete(w.state, key)
		return true
	}
	st.cooldownUntil = w.now().Add(warmFailureCooldown)
	return true
}

func (w *SchemaWarmer) snapshotOnWarmed() func(string, string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.onWarmed
}

func (w *SchemaWarmer) snapshotOnWarmError() func(string, string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.onWarmError
}

// tableEntries projects the eager value list from the loaded *models.Table
// slice. *models.Table embeds atomic counters and must not be value-copied
// (store Finding M); only the plain Name and Kind fields are read into a flat
// TableEntry so the snapshot carries the view-vs-table distinction without
// holding a *models.Table.
func tableEntries(tables []*models.Table) []TableEntry {
	out := make([]TableEntry, 0, len(tables))
	for _, t := range tables {
		if t == nil {
			continue
		}
		out = append(out, TableEntry{Name: t.Name, Kind: t.Kind})
	}
	return out
}
