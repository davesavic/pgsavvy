package keys

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
)

func noopExHandler(_ []string, _ commands.ExecCtx) error { return nil }

func TestExRegistry_RegisterThenGet(t *testing.T) {
	r := NewExRegistry()
	called := false
	cmd := ExCommand{
		Name:        "reload",
		Description: "reload user config",
		Handler: func(_ []string, _ commands.ExecCtx) error {
			called = true
			return nil
		},
	}
	if err := r.Register(cmd); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, ok := r.Get("reload")
	if !ok {
		t.Fatal("Get(reload): not found")
	}
	if got.Name != "reload" || got.Description != "reload user config" {
		t.Errorf("Get returned %+v, want name=reload desc=reload user config", got)
	}
	if err := got.Handler(nil, commands.ExecCtx{}); err != nil {
		t.Fatalf("handler invocation: %v", err)
	}
	if !called {
		t.Error("handler was not invoked")
	}
	if !r.Has("reload") {
		t.Error("Has(reload) = false")
	}
}

func TestExRegistry_DuplicateRejected(t *testing.T) {
	r := NewExRegistry()
	if err := r.Register(ExCommand{Name: "reload", Handler: noopExHandler}); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	err := r.Register(ExCommand{Name: "reload", Handler: noopExHandler})
	if !errors.Is(err, ErrDuplicateExCommand) {
		t.Fatalf("duplicate Register err = %v, want errors.Is(ErrDuplicateExCommand)", err)
	}
	// The error should name the offending command.
	if msg := err.Error(); msg == "" {
		t.Fatal("duplicate err has empty message")
	}
}

func TestExRegistry_EmptyNameRejected(t *testing.T) {
	r := NewExRegistry()
	err := r.Register(ExCommand{Name: "", Handler: noopExHandler})
	if !errors.Is(err, ErrInvalidExCommandName) {
		t.Errorf("empty-name Register err = %v, want ErrInvalidExCommandName", err)
	}
}

func TestExRegistry_NilHandlerRejected(t *testing.T) {
	r := NewExRegistry()
	err := r.Register(ExCommand{Name: "reload", Handler: nil})
	if !errors.Is(err, ErrNilExCommandHandler) {
		t.Errorf("nil-handler Register err = %v, want ErrNilExCommandHandler", err)
	}
}

func TestExRegistry_GetMissing(t *testing.T) {
	r := NewExRegistry()
	if _, ok := r.Get("nope"); ok {
		t.Error("Get(nope) = ok, want missing")
	}
	if r.Has("nope") {
		t.Error("Has(nope) = true, want false")
	}
}

func TestExRegistry_ListIsSorted(t *testing.T) {
	r := NewExRegistry()
	names := []string{"reload", "alpha", "zebra", "mango"}
	for _, n := range names {
		if err := r.Register(ExCommand{Name: n, Handler: noopExHandler}); err != nil {
			t.Fatalf("Register(%q): %v", n, err)
		}
	}
	got := r.List()
	want := append([]string(nil), names...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("List() = %v, want %v", got, want)
	}
}

// Case-insensitive: registering "set" matches Get("SET"), Get("Set"), etc.
func TestExRegistry_CaseInsensitive(t *testing.T) {
	r := NewExRegistry()
	if err := r.Register(ExCommand{Name: "set", Handler: noopExHandler}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	for _, name := range []string{"set", "SET", "Set", "sEt"} {
		if _, ok := r.Get(name); !ok {
			t.Errorf("Get(%q) = not found, want found", name)
		}
		if !r.Has(name) {
			t.Errorf("Has(%q) = false, want true", name)
		}
	}
}

// Registering the same name in different cases is a duplicate.
func TestExRegistry_CaseInsensitiveDuplicate(t *testing.T) {
	r := NewExRegistry()
	if err := r.Register(ExCommand{Name: "set", Handler: noopExHandler}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	err := r.Register(ExCommand{Name: "SET", Handler: noopExHandler})
	if !errors.Is(err, ErrDuplicateExCommand) {
		t.Fatalf("duplicate Register err = %v, want errors.Is(ErrDuplicateExCommand)", err)
	}
}

// Concurrent Register + Get under -race: a short burst is enough; this
// exercises the RWMutex without flake risk.
func TestExRegistry_Concurrent(t *testing.T) {
	r := NewExRegistry()
	const N = 64
	var wg sync.WaitGroup
	wg.Add(2 * N)
	for i := range N {
		name := fmt.Sprintf("cmd-%d", i)
		go func(n string) {
			defer wg.Done()
			_ = r.Register(ExCommand{Name: n, Handler: noopExHandler})
		}(name)
		go func(n string) {
			defer wg.Done()
			_, _ = r.Get(n)
		}(name)
	}
	wg.Wait()
	// Every Register call had a unique name, so List should now contain
	// exactly N entries.
	if got := len(r.List()); got != N {
		t.Errorf("after concurrent Register: List len = %d, want %d", got, N)
	}
}
