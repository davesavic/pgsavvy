package orchestrator

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// TestWiringInvariant is the source-of-truth checklist for Context wiring.
// It enumerates EVERY types.ContextKey (via types.AllContextKeys) and, per
// the key's ContextKind, asserts the wiring contract:
//
//   - the key is present in the ContextTree (keyed by GetKey over
//     Flatten()), unless explicitly allowlisted as deferred;
//   - if it is a popup that renders through the Tier-3 popupRectFor loop
//     (TEMPORARY_POPUP or DISPLAY_CONTEXT, minus the dedicated-overlay
//     exceptions), popupRectFor returns (rect, true) with a NON-ZERO rect
//     against a representative dims map;
//   - if it is a renderable context (SIDE/MAIN/EXTRAS/TEMPORARY_POPUP/
//     PERSISTENT_POPUP/DISPLAY), its concrete type DECLARES its own
//     HandleRender (i.e. does not inherit BaseContext's no-op), unless
//     explicitly allowlisted.
//
// Adding a new ContextKey without wiring it (missing from Flatten, missing
// a popupRectFor case, or relying on BaseContext's no-op HandleRender)
// makes this test fail. Deliberate exceptions live in the commented
// allowlists below, each citing WHY.
//
// Enumeration approach: Go has no enum reflection, so the keys are listed
// once in types.AllContextKeys() — adjacent to the const block so the list
// is visible in review. This is simpler than go/ast-parsing the const
// block and keeps a single maintenance site.
//
// HandleRender detection approach: reflection on method values is NOT
// usable here. Through the IBaseContext interface every dynamic type yields
// the same method-wrapper code pointer, and a promoted (inherited) method
// from an embedded BaseContext gets its own forwarding-wrapper pointer that
// reflect/runtime still names after the promoting type — so neither
// reflect.Value.Pointer() nor runtime.FuncForPC can distinguish "overrides
// HandleRender" from "inherits the no-op". Behaviour probing is equally
// unreliable (most real renders wrap writes in driver.Update closures or
// early-return on empty state, so they write nothing under a recorder).
// The only deterministic signal is structural: parse the context package
// source and collect the concrete types that DECLARE their own
// HandleRender method receiver. A renderable context whose type is absent
// from that set inherits BaseContext's no-op and is therefore unwired.
func TestWiringInvariant(t *testing.T) {
	tree := context.NewContextTree(types.ContextTreeDeps{})

	// Index every flattened context by its key.
	byKey := map[types.ContextKey]types.IBaseContext{}
	for _, c := range tree.Flatten() {
		byKey[c.GetKey()] = c
	}

	// Concrete types that DECLARE their own HandleRender (see doc comment).
	declaresHandleRender := contextTypesDeclaringHandleRender(t)

	// 100×100 popup-overlay canvas so percentage-based rects land at
	// non-zero sizes (mirrors popup_rect_for_test.go).
	canvas := ui.Dimensions{X0: 0, Y0: 0, X1: 100, Y1: 100}
	dims := map[string]ui.Dimensions{"popup-overlay": canvas}

	// presenceAllowlist: keys deliberately NOT in Flatten(). Each entry
	// cites why.
	presenceAllowlist := map[types.ContextKey]string{
		// COLUMNS/INDEXES retain ContextTree struct fields (Kind=STUB) but
		// are intentionally absent from Flatten(): the standalone
		// columns/indexes side rails were superseded by the TABLE_INSPECT
		// tabbed popup (epic dbsavvy-3vf), so they are deferred and never
		// pushed/rendered on their own.
		types.COLUMNS: "superseded by TABLE_INSPECT popup (epic dbsavvy-3vf); deferred, not flattened",
		types.INDEXES: "superseded by TABLE_INSPECT popup (epic dbsavvy-3vf); deferred, not flattened",
	}

	// popupRectAllowlist: popup-kind keys (TEMPORARY_POPUP/DISPLAY_CONTEXT)
	// that deliberately do NOT route through popupRectFor.
	popupRectAllowlist := map[types.ContextKey]string{
		// LIMIT renders full-canvas via renderLimitOverlay (the
		// terminal-too-small overlay), not the Tier-3 popupRectFor loop.
		types.LIMIT: "renders via dedicated renderLimitOverlay path, not popupRectFor",
		// WHICH_KEY renders bottom-right via its dedicated which-key overlay
		// path, not popupRectFor.
		types.WHICH_KEY: "renders via dedicated which-key overlay path, not popupRectFor",
		// SUGGESTIONS is the cursor-anchored completion dropdown
		// (dbsavvy-etp.2): its rect is computed at the orchestrator call
		// site from the live editor view geometry + the context anchor,
		// because popupRectFor lacks access to the view handle / anchor.
		// popupRectFor returns no rect for the PopupSizeAnchored kind.
		types.SUGGESTIONS: "cursor-anchored; rect computed at call site from editor view geometry, not popupRectFor",
	}

	// renderAllowlist: renderable-kind keys that deliberately inherit
	// BaseContext's no-op HandleRender (deferred skeletons / alternate
	// render paths). Each entry cites why.
	renderAllowlist := map[types.ContextKey]string{
		// QUERY_EDITOR is the live MAIN pane but its content is painted via
		// the editor.Buffer render path in Layout, not its own
		// HandleRender; the concrete editor wiring is deferred (epic
		// dbsavvy-wwd child tasks).
		types.QUERY_EDITOR: "deferred; renders via editor.Buffer path in Layout, not HandleRender",
		// MENU is a T2 lifecycle skeleton; popup body is populated by the
		// menu helper in a later epic, not via HandleRender.
		types.MENU: "deferred skeleton; body populated by menu helper in later epic",
		// CONFIRMATION is a T2 lifecycle skeleton; its body is written by
		// the confirm helper, not via HandleRender.
		types.CONFIRMATION: "deferred skeleton; body populated by confirm helper in later epic",
		// MESSAGES is a T2 EXTRAS skeleton; panel content (PG NOTICE/audit)
		// is routed in by later epics, not via HandleRender.
		types.MESSAGES: "deferred skeleton; panel content routed by later epic, not HandleRender",
	}

	// renderableKinds are the kinds that own a view and must render
	// something. STUB (deferred) and GLOBAL_CONTEXT (viewless) are excluded.
	renderableKinds := map[types.ContextKind]bool{
		types.SIDE_CONTEXT:     true,
		types.MAIN_CONTEXT:     true,
		types.EXTRAS_CONTEXT:   true,
		types.TEMPORARY_POPUP:  true,
		types.PERSISTENT_POPUP: true,
		types.DISPLAY_CONTEXT:  true,
	}

	// Enumerate from the DERIVED context-spec table (dbsavvy-fow.11 D3a),
	// observed via the live tree NewContextTree builds from contextSpecs —
	// not from a hand-listed key slice. The flattened contexts give every
	// spec row with inFlatten=true; the two named-only fields (Columns /
	// Indexes, inFlatten=false) are appended so the spec key set is complete.
	// specKeys is therefore the single source of truth's key column, read
	// back through the public ContextTree surface.
	specKeys := contextSpecKeysFromTree(tree)

	// Tie the derived spec key set to the canonical const-derived list so
	// neither can drift: types.AllContextKeys() must enumerate exactly the
	// keys the spec table wires (TestAllContextKeysComplete already pins
	// AllContextKeys to the const block, so this transitively pins the spec
	// table to the consts). A spec row added without an AllContextKeys entry
	// — or vice versa — fails here.
	assertKeySetsEqual(t, specKeys, types.AllContextKeys())

	for _, key := range specKeys {
		ctx, present := byKey[key]

		// 1. Presence in Flatten().
		if !present {
			if why, ok := presenceAllowlist[key]; ok {
				t.Logf("ALLOWLIST presence %s: %s", key, why)
				continue
			}
			t.Errorf("%s: missing from ContextTree.Flatten()", key)
			continue
		}

		kind := ctx.GetKind()

		// 2. popupRectFor for popup-rendered kinds.
		if kind == types.TEMPORARY_POPUP || kind == types.DISPLAY_CONTEXT {
			if why, ok := popupRectAllowlist[key]; ok {
				t.Logf("ALLOWLIST popupRect %s: %s", key, why)
			} else {
				r, ok := popupRectFor(key, dims, 100, 100)
				if !ok {
					t.Errorf("%s (kind=%d): popupRectFor returned ok=false; no rect case", key, kind)
				} else if r == (rect{}) {
					t.Errorf("%s (kind=%d): popupRectFor returned zero rect %+v", key, kind, r)
				}
			}
		}

		// 3. Declared (non-no-op) HandleRender for renderable kinds.
		if renderableKinds[kind] {
			if why, ok := renderAllowlist[key]; ok {
				t.Logf("ALLOWLIST render %s: %s", key, why)
				continue
			}
			typeName := reflect.TypeOf(ctx).Elem().Name()
			if !declaresHandleRender[typeName] {
				t.Errorf("%s (kind=%d, %s): inherits BaseContext's no-op HandleRender; not wired",
					key, kind, typeName)
			}
		}
	}
}

// contextSpecKeysFromTree reconstructs the full key column of the derived
// context-spec table from the public ContextTree surface: every flattened
// context's key (inFlatten=true rows) plus the two named-only fields
// (Columns / Indexes, inFlatten=false) that retain a struct field but are
// excluded from Flatten(). This reads the single-source-of-truth keys back
// through derived data rather than re-listing them.
func contextSpecKeysFromTree(tree *context.ContextTree) []types.ContextKey {
	out := make([]types.ContextKey, 0)
	for _, c := range tree.Flatten() {
		out = append(out, c.GetKey())
	}
	// Named-only fields excluded from Flatten() (inFlatten=false).
	out = append(out, tree.Columns.GetKey(), tree.Indexes.GetKey())
	return out
}

// assertKeySetsEqual fails the test if got and want are not the same SET of
// keys (order-independent, duplicate-detecting). Used to pin the derived
// spec key set to types.AllContextKeys() so neither drifts.
func assertKeySetsEqual(t *testing.T, got, want []types.ContextKey) {
	t.Helper()
	gotSet := map[types.ContextKey]int{}
	for _, k := range got {
		gotSet[k]++
	}
	wantSet := map[types.ContextKey]int{}
	for _, k := range want {
		wantSet[k]++
	}
	for k, n := range gotSet {
		if n > 1 {
			t.Errorf("context-spec key %q appears %d times in the spec table (duplicate row)", k, n)
		}
		if wantSet[k] == 0 {
			t.Errorf("context-spec key %q is wired in the spec table but missing from types.AllContextKeys()", k)
		}
	}
	for k := range wantSet {
		if gotSet[k] == 0 {
			t.Errorf("types.AllContextKeys() lists %q but the context-spec table has no row for it", k)
		}
	}
}

// TestAllContextKeysComplete guards the enumeration itself: every ContextKey
// constant declared in pkg/gui/types/context.go must appear in
// types.AllContextKeys(). Without this guard, adding a new ContextKey const
// but forgetting to list it in AllContextKeys() would silently bypass
// TestWiringInvariant (which only iterates the listed keys) — re-opening the
// exact "forgot a wiring site, fails silently" failure mode this epic closes.
// Source-parsed because Go has no enum reflection.
func TestAllContextKeysComplete(t *testing.T) {
	declared := contextKeyConstsDeclared(t)

	listed := map[string]bool{}
	for _, k := range types.AllContextKeys() {
		listed[string(k)] = true
	}

	for name, val := range declared {
		if !listed[val] {
			t.Errorf("ContextKey const %s (%q) is declared in types/context.go but missing from types.AllContextKeys()", name, val)
		}
	}
}

// contextKeyConstsDeclared parses pkg/gui/types/context.go and returns the
// declared ContextKey constants as name->value. Every constant in the block
// carries an explicit `ContextKey` type, so each ValueSpec is self-describing.
func contextKeyConstsDeclared(t *testing.T) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "../types/context.go", nil, 0)
	if err != nil {
		t.Fatalf("parse types/context.go: %v", err)
	}
	out := map[string]string{}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, s := range gd.Specs {
			vs, ok := s.(*ast.ValueSpec)
			if !ok {
				continue
			}
			id, ok := vs.Type.(*ast.Ident)
			if !ok || id.Name != "ContextKey" {
				continue
			}
			if len(vs.Names) == 0 || len(vs.Values) == 0 {
				continue
			}
			lit, ok := vs.Values[0].(*ast.BasicLit)
			if !ok {
				continue
			}
			val, err := strconv.Unquote(lit.Value)
			if err != nil {
				continue
			}
			out[vs.Names[0].Name] = val
		}
	}
	if len(out) == 0 {
		t.Fatal("parsed types/context.go but found no ContextKey constants")
	}
	return out
}

// contextTypesDeclaringHandleRender parses the sibling context package
// source and returns the set of concrete type names that declare their own
// HandleRender method receiver. Types absent from this set that embed
// BaseContext inherit its no-op. The path is relative to the test's working
// directory, which `go test` sets to this package's directory.
func contextTypesDeclaringHandleRender(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, "../context", nil, 0)
	if err != nil {
		t.Fatalf("parse context package: %v", err)
	}
	declared := map[string]bool{}
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, d := range f.Decls {
				fn, ok := d.(*ast.FuncDecl)
				if !ok || fn.Recv == nil || fn.Name.Name != "HandleRender" {
					continue
				}
				if len(fn.Recv.List) == 0 {
					continue
				}
				if star, ok := fn.Recv.List[0].Type.(*ast.StarExpr); ok {
					if id, ok := star.X.(*ast.Ident); ok {
						declared[id.Name] = true
					}
				}
			}
		}
	}
	if len(declared) == 0 {
		t.Fatal("parsed context package but found no HandleRender declarations")
	}
	return declared
}
