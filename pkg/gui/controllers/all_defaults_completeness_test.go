package controllers

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// keybindingProvider is the method set every controller that contributes
// bindings to the trie implements. A Controllers field whose type
// satisfies this interface MUST be appended in AllDefaultBindings,
// otherwise its bindings never reach the keybinding service and the keys
// silently do nothing (dbsavvy-9v1.3 guard 2).
type keybindingProvider interface {
	GetKeybindings(types.KeybindingsOpts) []*types.ChordBinding
}

var keybindingProviderType = reflect.TypeOf((*keybindingProvider)(nil)).Elem()

// TestAllDefaultBindingsIncludesEveryProviderController guards the
// hand-maintained AllDefaultBindings list against omission. It reflects
// over the Controllers struct to find every field whose type implements
// the keybinding-provider method, then asserts each such field is
// referenced (as `c.<Field>`) inside AllDefaultBindings's source body.
//
// A controller added to the Controllers struct but forgotten in
// AllDefaultBindings would publish no bindings to the trie — the exact
// silent-drop failure mode this guard closes. Deliberate exclusions go
// in providerAllowlist below, each citing why.
//
// Enumeration approach: reflection over struct fields for the provider
// set (Go gives us the field type's method set directly) + go/ast parse
// of all_defaults.go for the referenced set. AST-parsing the function
// body is the only way to observe which fields AllDefaultBindings
// actually appends, since the appends are imperative statements, not
// reflectable data. This mirrors the enumerate+assert+allowlist house
// style of orchestrator/wiring_invariant_test.go.
func TestAllDefaultBindingsIncludesEveryProviderController(t *testing.T) {
	// providerAllowlist: Controllers fields whose type implements the
	// provider interface but are deliberately NOT appended in
	// AllDefaultBindings. Empty today; entries cite why.
	providerAllowlist := map[string]string{}

	// 1. Reflect: every Controllers field whose type is a keybinding provider.
	ctrlType := reflect.TypeOf(Controllers{})
	providerFields := map[string]bool{}
	for i := 0; i < ctrlType.NumField(); i++ {
		f := ctrlType.Field(i)
		if f.Type.Implements(keybindingProviderType) {
			providerFields[f.Name] = true
		}
	}
	if len(providerFields) == 0 {
		t.Fatal("reflected Controllers struct but found no keybinding-provider fields")
	}

	// 2. AST: every `c.<Field>` referenced inside AllDefaultBindings.
	referenced := referencedControllerFields(t, "AllDefaultBindings")

	// 3. Assert every provider field is referenced (or allowlisted).
	for name := range providerFields {
		if referenced[name] {
			continue
		}
		if why, ok := providerAllowlist[name]; ok {
			t.Logf("ALLOWLIST %s: %s", name, why)
			continue
		}
		t.Errorf("Controllers.%s implements GetKeybindings but is not appended in AllDefaultBindings; its keybindings never reach the trie", name)
	}
}

// referencedControllerFields parses all_defaults.go and returns the set
// of receiver-field names referenced as `<recv>.<Field>` inside the named
// function. The receiver is the function's sole *Controllers parameter.
func referencedControllerFields(t *testing.T, fnName string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "all_defaults.go", nil, 0)
	if err != nil {
		t.Fatalf("parse all_defaults.go: %v", err)
	}

	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == fnName {
			fn = fd
			break
		}
	}
	if fn == nil {
		t.Fatalf("function %s not found in all_defaults.go", fnName)
	}

	// Identify the *Controllers parameter name (the receiver-like ident
	// every `c.Field` selector is rooted at).
	recvName := ""
	for _, p := range fn.Type.Params.List {
		star, ok := p.Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		if id, ok := star.X.(*ast.Ident); ok && id.Name == "Controllers" && len(p.Names) > 0 {
			recvName = p.Names[0].Name
		}
	}
	if recvName == "" {
		t.Fatalf("%s has no *Controllers parameter", fnName)
	}

	referenced := map[string]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == recvName {
			referenced[sel.Sel.Name] = true
		}
		return true
	})
	if len(referenced) == 0 {
		t.Fatalf("parsed %s but found no %s.<Field> references", fnName, recvName)
	}
	return referenced
}
