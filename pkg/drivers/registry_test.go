package drivers

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

func newStubFactory() Factory {
	return func(_ context.Context) (Driver, error) { return nil, nil }
}

func TestRegister_EmptyNamePanics(t *testing.T) {
	resetRegistry()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for empty name")
		}
	}()
	Register("", newStubFactory())
}

func TestRegister_NilFactoryPanics(t *testing.T) {
	resetRegistry()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil factory")
		}
	}()
	Register("postgres", nil)
}

func TestRegister_DoubleRegistrationPanicsWithMessage(t *testing.T) {
	resetRegistry()
	Register("postgres", newStubFactory())
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for duplicate registration")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("expected string panic, got %T: %v", r, r)
		}
		if !strings.Contains(msg, "postgres") || !strings.Contains(msg, "already registered") {
			t.Fatalf("panic message %q must contain 'postgres' and 'already registered'", msg)
		}
	}()
	Register("postgres", newStubFactory())
}

func TestGet_UnknownReturnsTypedError(t *testing.T) {
	resetRegistry()
	f, err := Get("nope")
	if f != nil {
		t.Fatal("expected nil factory on unknown driver")
	}
	if !errors.Is(err, ErrUnknownDriver) {
		t.Fatalf("expected ErrUnknownDriver, got %v", err)
	}
}

func TestGet_KnownReturnsFactory(t *testing.T) {
	resetRegistry()
	Register("postgres", newStubFactory())
	f, err := Get("postgres")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f == nil {
		t.Fatal("expected non-nil factory")
	}
}

func TestNames_ReturnsAlphaSortedSnapshotFromNonAlphaInsertion(t *testing.T) {
	resetRegistry()
	for _, n := range []string{"postgres", "mysql", "sqlite", "duckdb"} {
		Register(n, newStubFactory())
	}
	got := Names()
	want := []string{"duckdb", "mysql", "postgres", "sqlite"}
	if len(got) != len(want) {
		t.Fatalf("len mismatch: got %v want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("at %d: got %q want %q", i, got[i], want[i])
		}
	}
	got[0] = "MUTATED"
	if Names()[0] == "MUTATED" {
		t.Fatal("Names() snapshot must not be mutated by callers")
	}
}

func TestRegisterGetNames_ConcurrentNoRace(t *testing.T) {
	resetRegistry()
	var wg sync.WaitGroup
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			Register(name, newStubFactory())
		}(n)
	}
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = Get("a")
			_ = Names()
		}()
	}
	wg.Wait()
}

// Compile-time sanity check (epic dbsavvy-921 D16): the Factory underlying
// signature matches func(context.Context) (Driver, error).
var _ Factory = (func(context.Context) (Driver, error))(nil)
