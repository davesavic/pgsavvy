package data

import (
	"errors"
	"path"

	"github.com/sirupsen/logrus"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/logs"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// ErrNeedsConfirmation is returned by UnhideSchema when the requested schema
// also matches at least one builtin or profile-level hide pattern. The caller
// (T7a schemas_controller) sees this sentinel via errors.Is, opens the
// unhide-confirmation popup (T9 i18n keys, T7b confirm_helper), and on user
// approval calls back into UnhideSchema with the runtime-only path forced —
// or simply mutates AppState directly. In either case the helper never
// silently overrides a builtin/profile rule.
//
// Exported (capitalized) per epic post-review M09d so cross-package callers
// (T7a controller + T11 integration test) can do errors.Is checks without
// importing an unexported sentinel.
var ErrNeedsConfirmation = errors.New("data: unhide needs confirmation (hidden by builtin or profile)")

// SchemasHelper owns the pure state-mutation surface for the SCHEMAS rail's
// hide/show feature. It does NOT touch the gui (gocui) layer — its consumers
// are the schemas controller (T7a, bindings) and the schemas context (T2,
// view-state flag). Filter logic, persistence (via AppStateStore.MutateAndSave
// + T3 debounce), and the per-context show-hidden toggle all live here.
//
// Construction: NewSchemasHelper(c, store) — the helper holds *common.Common
// (for the warn logger on malformed globs) and a *common.AppStateStore (for
// MutateAndSave + HiddenSchemasSnapshot). It does NOT need a worker queue:
// every method either reads a defensive snapshot or funnels writes through
// the store's serialized MutateAndSave, so all goroutine-safety concerns are
// already covered by T3.
type SchemasHelper struct {
	c     *common.Common
	store *common.AppStateStore
}

// NewSchemasHelper builds a SchemasHelper bound to the cross-cutting deps bag
// and the app-state store. Both arguments are required; nil is not supported
// (zero-value would dereference on first call, surfacing the wiring bug at
// the call site rather than masking it).
func NewSchemasHelper(c *common.Common, store *common.AppStateStore) *SchemasHelper {
	return &SchemasHelper{c: c, store: store}
}

// FilterHidden splits raw into (visible, hidden) using the three-way union of
// builtin ∪ profile ∪ runtime glob patterns. Matching uses path.Match against
// schema.Name ONLY (per epic NOTES M09g — Owner is irrelevant for hide).
//
// Guarantees:
//   - Preserves raw input order in both output slices.
//   - Dedupes the merged hidden set across builtin/profile/runtime overlap:
//     a schema that matches via multiple categories appears once in hidden.
//   - A malformed pattern (path.Match returns a non-nil error, e.g. "[") is
//     logged via c.Log.Warnf and SKIPPED — remaining patterns still apply
//     (M09f). The helper never fails on a bad glob.
//   - Empty raw → returns (nil-or-empty, nil-or-empty) without allocating
//     beyond the zero-length slice headers.
//
// Inputs:
//   - raw:     the full schema list as returned by ConnectHelper.LoadSchemas
//   - builtin: per-driver list (e.g. pg.BuiltinHiddenSchemas)
//   - profile: per-connection-profile list (UserConfig)
//   - runtime: per-process AppState.HiddenSchemas[connID] snapshot
func (h *SchemasHelper) FilterHidden(
	raw []models.Schema,
	builtin, profile, runtime []string,
) (visible, hidden []models.Schema) {
	if len(raw) == 0 {
		return nil, nil
	}

	// Pre-allocate to len(raw): worst-case one of the slices is full, the
	// other empty. Two small allocations beat repeated grows.
	visible = make([]models.Schema, 0, len(raw))
	hidden = make([]models.Schema, 0, len(raw))

	// Track which (raw) names we've already classified as hidden so the
	// "dedupe across overlapping categories" guarantee holds even when the
	// raw input itself contains duplicates (defensive — the upstream loader
	// is not promised to dedupe).
	seenHidden := make(map[string]struct{}, len(raw))

	for _, sch := range raw {
		if h.matchesAny(sch.Name, builtin) ||
			h.matchesAny(sch.Name, profile) ||
			h.matchesAny(sch.Name, runtime) {
			if _, dup := seenHidden[sch.Name]; dup {
				// Same name appeared in raw twice AND matches a hide rule —
				// suppress the duplicate from hidden, keep order in visible
				// untouched (we also don't push the dup into visible).
				continue
			}
			seenHidden[sch.Name] = struct{}{}
			hidden = append(hidden, sch)
			continue
		}
		visible = append(visible, sch)
	}
	return visible, hidden
}

// matchesAny reports whether name matches any pattern in patterns via
// path.Match. Malformed patterns are logged and skipped per M09f.
func (h *SchemasHelper) matchesAny(name string, patterns []string) bool {
	for _, pat := range patterns {
		ok, err := path.Match(pat, name)
		if err != nil {
			// path.Match returns ErrBadPattern for syntactically invalid
			// patterns ("[", "[^", etc). Skip — log once per encounter so
			// operators can spot the config bug, but DO NOT fail the filter.
			if h.c != nil && h.c.Log != nil {
				h.c.Log.Warnf("schemas_helper: skipping malformed hide pattern %q: %v", pat, err)
			}
			continue
		}
		if ok {
			return true
		}
	}
	return false
}

// HideSchema appends schemaName to AppState.HiddenSchemas[connID] via the
// store's debounced MutateAndSave. Idempotent: if the name is already present
// the mutation is a no-op (no duplicate entry). Returns nil on success;
// MutateAndSave swallows errStoreClosed into LastSaveErr() rather than
// surfacing here, so this helper has nothing to return beyond nil for the
// in-memory mutation result. The store's LastSaveErr() is the canonical
// place to observe persistence failures.
func (h *SchemasHelper) HideSchema(connID, schemaName string) error {
	h.store.MutateAndSave(func(s *common.AppState) {
		if s.HiddenSchemas == nil {
			s.HiddenSchemas = map[string][]string{}
		}
		existing := s.HiddenSchemas[connID]
		for _, n := range existing {
			if n == schemaName {
				// Idempotent: same connection ID + same schema name twice in
				// a row is a no-op. Crucially we still arm the debounce
				// timer (MutateAndSave does) so an empty mutation still
				// touches disk eventually — that's fine; AppState content
				// is unchanged.
				return
			}
		}
		s.HiddenSchemas[connID] = append(existing, schemaName)
	})
	logs.Event(h.logger(), "state", "schema_hide", logrus.Fields{"conn_id": connID, "schema": schemaName})
	return nil
}

// logger returns the per-session logger threaded through *common.Common,
// or nil if Common is unwired (test fixtures). logs.Event is nil-tolerant
// so this stays safe in both paths.
func (h *SchemasHelper) logger() *logrus.Logger {
	if h.c == nil {
		return nil
	}
	return h.c.Log
}

// UnhideSchema removes schemaName from AppState.HiddenSchemas[connID] via
// MutateAndSave. Before mutating it checks the schema against the merged
// (builtin ∪ profile) glob set: if ANY pattern in those two lists matches,
// the helper returns ErrNeedsConfirmation WITHOUT touching state. The caller
// is expected to surface the confirmation popup (Tr.UnhideConfirmationBody)
// and, on user approval, drive a different path (direct AppState mutation or
// a future ForceUnhideSchema).
//
// When the schema is hidden ONLY via the runtime layer (AppState), the
// removal proceeds silently; not-present → nil no-op.
//
// The builtin/profile glob lists are passed in explicitly so the predicate
// is testable in isolation (and so the helper does NOT import a concrete
// driver package — driver registry promotion lands in T-M11).
func (h *SchemasHelper) UnhideSchema(connID, schemaName string, builtin, profile []string) error {
	// Predicate runs BEFORE mutation. Order: builtin first (cheapest, smallest
	// list in practice), then profile. matchesAny logs malformed globs and
	// keeps going — same semantics as FilterHidden.
	if h.matchesAny(schemaName, builtin) || h.matchesAny(schemaName, profile) {
		return ErrNeedsConfirmation
	}

	var removed bool
	h.store.MutateAndSave(func(s *common.AppState) {
		if s.HiddenSchemas == nil {
			return
		}
		existing, ok := s.HiddenSchemas[connID]
		if !ok {
			return
		}
		out := existing[:0:0] // fresh backing array — avoid mutating the snapshot
		for _, n := range existing {
			if n == schemaName {
				removed = true
				continue
			}
			out = append(out, n)
		}
		if !removed {
			// No-op + nil contract: name wasn't in the runtime layer, no
			// builtin/profile hit either (we'd have returned above), so just
			// leave the slice as-is. The debounced save still re-fires but
			// the YAML content is byte-identical.
			return
		}
		if len(out) == 0 {
			// Empty slice → drop the key entirely so the YAML stays tidy.
			delete(s.HiddenSchemas, connID)
			return
		}
		s.HiddenSchemas[connID] = out
	})
	// AC: emit ONLY on actual removal — skip the not-present no-op and the
	// ErrNeedsConfirmation early return (handled above).
	if removed {
		logs.Event(h.logger(), "state", "schema_unhide", logrus.Fields{"conn_id": connID, "schema": schemaName})
	}
	return nil
}

// ToggleShowHidden flips the per-context show-hidden flag on the supplied
// SchemasContext. This is purely view-state — it is NEVER persisted (per
// DESIGN.md §15.9 and epic AC). Concurrency: SchemasContext owns its own
// access discipline; ToggleShowHidden simply calls Get + Set under the
// caller's goroutine (typically the gocui main loop).
func (h *SchemasHelper) ToggleShowHidden(ctx *context.SchemasContext) {
	if ctx == nil {
		return
	}
	ctx.SetShowHiddenMode(!ctx.GetShowHiddenMode())
}
