package orchestrator_test

import (
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// TestShippedDefaultsHaveNoOrphanActions is the hard test gate for
// dbsavvy-9v1.3 guard 1. At runtime an ActionID that resolves to no
// registered handler is only a WarnLevel `orphan_action` warning and the
// binding is silently dropped (keys/keybinding_service.go:287-294,
// :315-324). This test builds the REAL shipped defaults
// (AllDefaultBindings) against the REAL action registry (the fully wired
// Gui) and FAILS if any orphan_action warning is produced — turning a
// silent drop into a build-time failure.
//
// The fully wired Gui is the faithful source: it constructs every
// controller (including the orchestrator-only TableInspect / CellEditor /
// CommitDialog / ConflictDialog / FKReversePicker / Cheatsheet) and
// registers every action, then runs svc.Build during wireWithDriver,
// stashing the warnings on g.Warnings().
func TestShippedDefaultsHaveNoOrphanActions(t *testing.T) {
	g, _ := buildTestGui(t)
	t.Cleanup(func() { _ = g.Close() })

	var orphans []string
	for _, w := range g.Warnings() {
		if w.Code == "orphan_action" {
			orphans = append(orphans, w.Message+" (origin: "+w.Origin+")")
		}
	}
	if len(orphans) > 0 {
		t.Fatalf("shipped default bindings produced %d orphan_action warning(s); every default ActionID must resolve to a registered handler:\n  %s",
			len(orphans), strings.Join(orphans, "\n  "))
	}
}

// TestShippedDefaultsListActionIDsDoNotCrossScopes is dbsavvy-9v1.3 guard
// 3a, scoped to the invariant that actually holds: a list-rail ActionID
// (list.up / list.down / list.confirm, in their per-rail
// `<prefix>:<scope>` form) must be bound in EXACTLY ONE scope. A list
// ActionID appearing in two scopes is the dbsavvy-6m9 cross-rail-dispatch
// collision — one chord routing to a handler registered for a different
// rail's cursor.
//
// Why not a blanket "ActionID unique per (Mode, Scope)" assertion: the
// shipped defaults intentionally alias multiple chords onto one ActionID
// within a single (Mode, Scope) — e.g. confirm.yes is bound to both `y`
// and <Enter>; hide_overlay.down to <down>, `j`, and `n`; vim text
// objects to `i{` and `iB`. Those are synonym chords, not collisions:
// both dispatch to the same handler in the same context with no
// ambiguity. Likewise, the rail.switch.* navigation actions are
// deliberately bound in every rail scope. The list-action cross-scope
// rule is the precise, holding form of the "collision-that-implies-
// cross-rail-dispatch" invariant the task targets.
func TestShippedDefaultsListActionIDsDoNotCrossScopes(t *testing.T) {
	g, _ := buildTestGui(t)
	t.Cleanup(func() { _ = g.Close() })

	defaults := controllers.AllDefaultBindings(g.Controllers())
	if len(defaults) == 0 {
		t.Fatal("AllDefaultBindings returned no bindings on a wired Gui")
	}

	listPrefixes := map[string]bool{
		commands.ListUp + ":":      true,
		commands.ListDown + ":":    true,
		commands.ListConfirm + ":": true,
	}
	isListAction := func(id string) bool {
		for p := range listPrefixes {
			if strings.HasPrefix(id, p) {
				return true
			}
		}
		return false
	}

	scopesByAction := map[string]map[types.ContextKey]bool{}
	for _, b := range defaults {
		if b == nil || !isListAction(b.ActionID) {
			continue
		}
		if scopesByAction[b.ActionID] == nil {
			scopesByAction[b.ActionID] = map[types.ContextKey]bool{}
		}
		scopesByAction[b.ActionID][b.Scope] = true
	}
	if len(scopesByAction) == 0 {
		t.Fatal("no list.* per-rail ActionIDs found in shipped defaults")
	}

	for action, scopes := range scopesByAction {
		if len(scopes) > 1 {
			var ss []string
			for s := range scopes {
				ss = append(ss, string(s))
			}
			t.Errorf("list ActionID %q is bound across multiple scopes %v — implies cross-rail dispatch (dbsavvy-6m9). Each rail must own a scope-distinct list ActionID",
				action, ss)
		}
	}
}

// TestShippedDefaultsListBindingsArePerRail is dbsavvy-9v1.3 guard 3b and
// the dbsavvy-6m9 regression guard at the binding-data layer. The
// list-style rail bindings (j=ListDown, k=ListUp, <CR>=ListConfirm) must
// each carry a PER-RAIL (scope-distinct) ActionID: the SCHEMAS j-binding's
// ActionID must differ from the CONNECTIONS j-binding's ActionID. Before
// the fix all rails shared one global ListDown ActionID, so j on any rail
// moved the CONNECTIONS cursor (dbsavvy-6m9). listActionID composes
// `list.down:CONNECTIONS`, `list.down:SCHEMAS`, … so each rail's chord
// resolves to its own handler.
//
// COLUMNS / INDEXES rails were superseded by the TABLE_INSPECT tabbed
// popup (epic dbsavvy-3vf) and are not constructed as standalone rails;
// the shipped side rails are CONNECTIONS, SCHEMAS, TABLES. We assert the
// per-rail invariant across whichever of those three rails publish list
// bindings, and require at least the two-rail j-collision case
// (CONNECTIONS vs SCHEMAS) the regression was about.
func TestShippedDefaultsListBindingsArePerRail(t *testing.T) {
	g, _ := buildTestGui(t)
	t.Cleanup(func() { _ = g.Close() })

	defaults := controllers.AllDefaultBindings(g.Controllers())

	// The three shipped side rails.
	railScopes := map[types.ContextKey]bool{
		types.CONNECTIONS: true,
		types.SCHEMAS:     true,
		types.TABLES:      true,
	}

	// For each list-action prefix, collect (scope -> ActionID) seen on
	// rail-scoped bindings whose ActionID is the per-rail form
	// `<prefix>:<scope>`.
	prefixes := []string{commands.ListDown, commands.ListUp, commands.ListConfirm}
	for _, prefix := range prefixes {
		perScope := map[types.ContextKey]string{}
		for _, b := range defaults {
			if b == nil || !railScopes[b.Scope] {
				continue
			}
			if !strings.HasPrefix(b.ActionID, prefix+":") {
				continue
			}
			if prev, ok := perScope[b.Scope]; ok && prev != b.ActionID {
				t.Errorf("%s rail has two %s ActionIDs (%q vs %q)", b.Scope, prefix, prev, b.ActionID)
			}
			perScope[b.Scope] = b.ActionID
		}

		// Every rail's ActionID for this prefix must be distinct from
		// every other rail's (per-rail dispatch invariant).
		byAction := map[string][]types.ContextKey{}
		for scope, action := range perScope {
			byAction[action] = append(byAction[action], scope)
		}
		for action, scopes := range byAction {
			if len(scopes) > 1 {
				t.Errorf("list action %q shared across rails %v — must be per-rail (dbsavvy-6m9)", action, scopes)
			}
		}

		// Sanity: the prefix must actually be bound on the rails (so this
		// guard cannot silently pass on an empty set).
		if len(perScope) == 0 {
			t.Errorf("no per-rail %s bindings found on any side rail; expected at least CONNECTIONS/SCHEMAS", prefix)
		}
	}

	// Explicit regression assertion: the CONNECTIONS j-binding and the
	// SCHEMAS j-binding must resolve to DIFFERENT ActionIDs. This is the
	// exact dbsavvy-6m9 case ("j on SCHEMAS moved the CONNECTIONS cursor").
	connDown := listActionForRail(defaults, types.CONNECTIONS, commands.ListDown)
	schemaDown := listActionForRail(defaults, types.SCHEMAS, commands.ListDown)
	if connDown == "" || schemaDown == "" {
		t.Fatalf("missing per-rail ListDown binding: connections=%q schemas=%q", connDown, schemaDown)
	}
	if connDown == schemaDown {
		t.Fatalf("CONNECTIONS and SCHEMAS share ListDown ActionID %q — j on SCHEMAS would move the CONNECTIONS cursor (dbsavvy-6m9 regression)", connDown)
	}
}

// listActionForRail returns the ActionID of the binding at scope whose
// ActionID is the per-rail form `<prefix>:<scope>`, or "" if absent.
func listActionForRail(defaults []*types.ChordBinding, scope types.ContextKey, prefix string) string {
	for _, b := range defaults {
		if b == nil || b.Scope != scope {
			continue
		}
		if strings.HasPrefix(b.ActionID, prefix+":") {
			return b.ActionID
		}
	}
	return ""
}
