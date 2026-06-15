package editor

import (
	"context"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/models"
)

// fakeDetailProvider is a test double for FunctionDetailProvider. It records
// FunctionDetail / WarmFunctionDetail calls and serves canned details, so the
// detail seam can be exercised without a live ConnectHelper.
type fakeDetailProvider struct {
	details   map[string][]models.FunctionDetail
	found     map[string]bool
	warmCalls []string
}

func newFakeDetailProvider() *fakeDetailProvider {
	return &fakeDetailProvider{
		details: map[string][]models.FunctionDetail{},
		found:   map[string]bool{},
	}
}

func (f *fakeDetailProvider) set(schema, name string, d []models.FunctionDetail) {
	f.details[schema+"\x00"+name] = d
	f.found[schema+"\x00"+name] = true
}

func (f *fakeDetailProvider) FunctionDetail(schema, name string) ([]models.FunctionDetail, bool) {
	key := schema + "\x00" + name
	return f.details[key], f.found[key]
}

func (f *fakeDetailProvider) WarmFunctionDetail(schema, name string, onReady func()) {
	f.warmCalls = append(f.warmCalls, schema+"\x00"+name)
	if onReady != nil {
		onReady()
	}
}

// TestSuggestion_HasSignatureField confirms the Signature field exists
// and is zero-valued by default — existing sources compile with it unset.
func TestSuggestion_HasSignatureField(t *testing.T) {
	var s Suggestion
	if s.Signature != "" {
		t.Fatalf("zero-value Suggestion.Signature = %q; want empty", s.Signature)
	}
	s.Signature = "f(a int) returns int"
	if s.Signature != "f(a int) returns int" {
		t.Fatalf("Signature round-trip failed: %q", s.Signature)
	}
}

// TestFunctionSource_DetailProviderSeam verifies the provider is held nil-safe
// and exposed via DetailProvider to consume.
func TestFunctionSource_DetailProviderSeam(t *testing.T) {
	src := NewFunctionSource(nil)
	if src.DetailProvider() != nil {
		t.Fatalf("DetailProvider() before injection = %v; want nil", src.DetailProvider())
	}

	p := newFakeDetailProvider()
	src.SetDetailProvider(p)
	if src.DetailProvider() == nil {
		t.Fatal("DetailProvider() after injection = nil; want the injected provider")
	}

	// The provider routes sync reads + async warms (the signature-population seam).
	p.set("public", "now", []models.FunctionDetail{{Name: "now"}})
	if _, ok := src.DetailProvider().FunctionDetail("public", "now"); !ok {
		t.Fatal("FunctionDetail(public, now) = not found; want hit")
	}
	if _, ok := src.DetailProvider().FunctionDetail("public", "missing"); ok {
		t.Fatal("FunctionDetail(public, missing) = found; want miss")
	}
	src.DetailProvider().WarmFunctionDetail("public", "lower", nil)
	if len(p.warmCalls) != 1 {
		t.Fatalf("warmCalls = %d; want 1", len(p.warmCalls))
	}
}

// TestFunctionSource_NilProvider_NoBehaviorChange asserts the provider-less
// source still emits the same function-name candidates with empty Signature —
// the invariant (no visible behavior change until the provider is consumed).
func TestFunctionSource_NilProvider_NoBehaviorChange(t *testing.T) {
	m := newFakeMeta()
	m.setFunctions("now", "lower")
	src := NewFunctionSource(m) // no provider wired

	got := src.Suggest(context.Background(), nil, Position{})
	if len(got) != 2 {
		t.Fatalf("len(got) = %d; want 2", len(got))
	}
	for _, sg := range got {
		if sg.Signature != "" {
			t.Errorf("%q Signature = %q; want empty (populated by 5.4, not 5.3)", sg.Text, sg.Signature)
		}
	}
}

// TestConnectHelperSatisfiesProvider is a compile-time-ish guard that the editor
// interface is satisfiable by a structural match (the concrete ConnectHelper in
// the orchestrator satisfies it the same way). We assert the fake — which has
// the exact method set the orchestrator wires — is assignable.
func TestConnectHelperSatisfiesProvider(t *testing.T) {
	var _ FunctionDetailProvider = newFakeDetailProvider()
}
