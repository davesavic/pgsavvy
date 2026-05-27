package session_test

import (
	"sync"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/session"
)

func TestSettingsSnapshot_GetSetDelete(t *testing.T) {
	s := session.NewSettingsSnapshot()
	s.Set("search_path", "public")

	v, ok := s.Get("search_path")
	if !ok || v != "public" {
		t.Fatalf("Get = (%q, %v), want (\"public\", true)", v, ok)
	}

	s.Delete("search_path")
	v, ok = s.Get("search_path")
	if ok {
		t.Fatalf("Get after Delete = (%q, %v), want (\"\", false)", v, ok)
	}
}

func TestSettingsSnapshot_AllReturnsCopy(t *testing.T) {
	s := session.NewSettingsSnapshot()
	s.Set("a", "1")
	s.Set("b", "2")

	all := s.All()
	all["a"] = "mutated"
	all["c"] = "injected"

	v, ok := s.Get("a")
	if !ok || v != "1" {
		t.Errorf("internal map affected by mutation of All() copy: a = %q", v)
	}
	if _, ok := s.Get("c"); ok {
		t.Error("internal map affected by insertion into All() copy")
	}
}

func TestSettingsSnapshot_GetMissingKey(t *testing.T) {
	s := session.NewSettingsSnapshot()
	v, ok := s.Get("nonexistent")
	if ok || v != "" {
		t.Fatalf("Get(missing) = (%q, %v), want (\"\", false)", v, ok)
	}
}

func TestSettingsSnapshot_DeleteMissingKey(t *testing.T) {
	s := session.NewSettingsSnapshot()
	s.Delete("nonexistent") // must not panic
}

func TestSettingsSnapshot_ConcurrentAccess(t *testing.T) {
	s := session.NewSettingsSnapshot()
	var wg sync.WaitGroup
	const n = 100

	for range n {
		wg.Add(2)
		go func() {
			defer wg.Done()
			s.Set("key", "val")
		}()
		go func() {
			defer wg.Done()
			s.Get("key")
		}()
	}
	wg.Wait()
}
