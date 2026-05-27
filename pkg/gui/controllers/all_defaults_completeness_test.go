package controllers

import (
	"reflect"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// keybindingProvider is the method set every controller that contributes
// bindings to the trie implements. A Controllers field whose type
// satisfies this interface MUST be listed in the per-controller registry
// (Controllers.entries()), otherwise AllDefaultBindings (which is DERIVED
// from that registry) never concatenates its GetKeybindings output and the
// keys silently do nothing (dbsavvy-9v1.3 guard 2 / dbsavvy-fow.11 D3a).
type keybindingProvider interface {
	GetKeybindings(types.KeybindingsOpts) []*types.ChordBinding
}

var keybindingProviderType = reflect.TypeOf((*keybindingProvider)(nil)).Elem()

// TestAllDefaultBindingsIncludesEveryProviderController guards the
// registry-derived AllDefaultBindings against omission. Now that
// AllDefaultBindings iterates Controllers.entries() instead of a
// hand-listed sequence of c.<Field> appends, the omission failure mode
// moved: a provider controller is dropped from the trie iff its field is
// missing from entries(). This guard reflects over the Controllers struct
// to find every field whose type implements the keybinding-provider method,
// then populates a Controllers value with EVERY such field set to a
// non-nil instance and asserts each appears in the derived entries() —
// equivalently, that AllDefaultBindings surfaces it.
//
// Coverage is equal-or-stronger than the prior AST-of-AllDefaultBindings
// approach: it still enumerates every provider field via reflection (the
// same set), but verifies the field is actually carried by the live
// registry the function consumes, rather than that a textual c.<Field>
// reference exists in a now-removed imperative body. A new controller field
// forgotten in entries() still fails. Deliberate exclusions go in
// providerAllowlist below, each citing why.
func TestAllDefaultBindingsIncludesEveryProviderController(t *testing.T) {
	// providerAllowlist: Controllers fields whose type implements the
	// provider interface but are deliberately NOT carried by entries().
	// Empty today; entries cite why.
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

	// 2. Build a Controllers value with every provider field set to a
	//    non-nil instance, so entries() (which skips typed-nil fields)
	//    surfaces all of them. reflect.New allocates a usable non-nil
	//    pointer for each *XController field; no controller method is
	//    invoked here, only struct membership in entries() is exercised.
	c := &Controllers{}
	cv := reflect.ValueOf(c).Elem()
	for name := range providerFields {
		fv := cv.FieldByName(name)
		if fv.Kind() != reflect.Ptr {
			t.Fatalf("provider field %s is not a pointer (%s); guard assumes *XController fields", name, fv.Kind())
		}
		fv.Set(reflect.New(fv.Type().Elem()))
	}

	// 3. Collect the field names carried by the derived registry.
	listed := map[string]bool{}
	for _, e := range c.entries() {
		listed[e.name] = true
	}
	if len(listed) == 0 {
		t.Fatal("Controllers.entries() returned no entries for a fully-populated bundle")
	}

	// 4. Assert every provider field is in the registry (or allowlisted).
	for name := range providerFields {
		if listed[name] {
			continue
		}
		if why, ok := providerAllowlist[name]; ok {
			t.Logf("ALLOWLIST %s: %s", name, why)
			continue
		}
		t.Errorf("Controllers.%s implements GetKeybindings but is not carried by entries(); AllDefaultBindings (derived from entries()) never surfaces it, so its keybindings never reach the trie", name)
	}

	// 5. Cross-check: every entry name is a real Controllers field with a
	//    provider type — guards against a typo'd or stale entries() name
	//    that would silently never match a struct field.
	for name := range listed {
		f, ok := ctrlType.FieldByName(name)
		if !ok {
			t.Errorf("entries() lists %q which is not a Controllers field", name)
			continue
		}
		if !f.Type.Implements(keybindingProviderType) {
			t.Errorf("entries() lists %q whose type %s does not implement GetKeybindings", name, f.Type)
		}
	}
}
