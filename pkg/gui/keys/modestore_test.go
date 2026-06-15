package keys

import (
	"fmt"
	"sync"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

func TestModeStore_Get_EmptyReturnsNormal(t *testing.T) {
	s := NewModeStore()
	if got := s.Get(types.GLOBAL); got != types.ModeNormal {
		t.Errorf("Get(GLOBAL) on empty store = %v, want ModeNormal", got)
	}
}

func TestModeStore_Get_DoesNotInsert(t *testing.T) {
	s := NewModeStore()
	_ = s.Get(types.QUERY_EDITOR)
	_ = s.Get(types.SCHEMAS)
	all := s.All()
	if len(all) != 0 {
		t.Errorf("All() after read-only Get calls = %v, want empty map", all)
	}
}

func TestModeStore_SetGet_RoundTrip(t *testing.T) {
	s := NewModeStore()
	s.Set(types.QUERY_EDITOR, types.ModeInsert)
	if got := s.Get(types.QUERY_EDITOR); got != types.ModeInsert {
		t.Errorf("Get(QUERY_EDITOR) = %v, want ModeInsert", got)
	}
}

func TestModeStore_KeyedIsolation(t *testing.T) {
	s := NewModeStore()
	s.Set(types.GLOBAL, types.ModeCommand)
	if got := s.Get(types.QUERY_EDITOR); got != types.ModeNormal {
		t.Errorf("Set on GLOBAL leaked into QUERY_EDITOR: got %v, want ModeNormal", got)
	}
	if got := s.Get(types.GLOBAL); got != types.ModeCommand {
		t.Errorf("Get(GLOBAL) = %v, want ModeCommand", got)
	}
}

func TestModeStore_Reset(t *testing.T) {
	s := NewModeStore()
	s.Set(types.QUERY_EDITOR, types.ModeInsert)
	s.Reset(types.QUERY_EDITOR)
	if got := s.Get(types.QUERY_EDITOR); got != types.ModeNormal {
		t.Errorf("Get after Reset = %v, want ModeNormal", got)
	}
	if all := s.All(); len(all) != 0 {
		t.Errorf("All() after Reset = %v, want empty", all)
	}
}

func TestModeStore_Reset_AbsentKey(t *testing.T) {
	s := NewModeStore()
	// Must not panic.
	s.Reset(types.QUERY_EDITOR)
	s.Reset(types.GLOBAL)
}

func TestModeStore_Concurrent(t *testing.T) {
	s := NewModeStore()
	const N = 100
	keys := make([]types.ContextKey, N)
	for i := range N {
		keys[i] = types.ContextKey(fmt.Sprintf("ctx-%d", i))
	}

	var wg sync.WaitGroup
	wg.Add(2 * N)
	for i := range N {
		k := keys[i]
		go func() {
			defer wg.Done()
			s.Set(k, types.ModeInsert)
		}()
	}
	for i := range N {
		k := keys[i]
		go func() {
			defer wg.Done()
			_ = s.Get(k)
		}()
	}
	wg.Wait()

	// All set keys observable.
	for i := range N {
		if got := s.Get(keys[i]); got != types.ModeInsert {
			t.Errorf("Get(%q) = %v, want ModeInsert", keys[i], got)
		}
	}
}

func TestModeStore_All_DefensiveCopy(t *testing.T) {
	s := NewModeStore()
	s.Set(types.GLOBAL, types.ModeCommand)
	snap := s.All()
	// Mutate the snapshot.
	snap[types.GLOBAL] = types.ModeInsert
	delete(snap, types.GLOBAL)
	snap[types.QUERY_EDITOR] = types.ModeReplace

	// Store unaffected.
	if got := s.Get(types.GLOBAL); got != types.ModeCommand {
		t.Errorf("Get(GLOBAL) after mutating snapshot = %v, want ModeCommand", got)
	}
	if got := s.Get(types.QUERY_EDITOR); got != types.ModeNormal {
		t.Errorf("Get(QUERY_EDITOR) after mutating snapshot = %v, want ModeNormal", got)
	}
}
