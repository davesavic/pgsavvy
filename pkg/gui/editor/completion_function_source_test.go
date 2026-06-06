package editor

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/drivers"
)

// funcFakeSession is a minimal drivers.Session that records the number
// of ListFunctions calls and returns the configured names / error.
// Every other Session method panics — FunctionSource must only call
// ListFunctions.
type funcFakeSession struct {
	drivers.Session // embed to satisfy the interface for unused methods

	names []string
	err   error
	calls atomic.Int32
}

func (f *funcFakeSession) ListFunctions(_ context.Context) ([]string, error) {
	f.calls.Add(1)
	return f.names, f.err
}

func TestFunctionSource_Identity(t *testing.T) {
	src := NewFunctionSource(nil)
	if src.Name() != FunctionSourceName {
		t.Errorf("Name() = %q; want %q", src.Name(), FunctionSourceName)
	}
	if src.Priority() != FunctionSourcePriority {
		t.Errorf("Priority() = %d; want %d", src.Priority(), FunctionSourcePriority)
	}
}

func TestFunctionSource_NilProvider_Empty(t *testing.T) {
	src := NewFunctionSource(nil)
	got := src.Suggest(context.Background(), nil, Position{})
	if got == nil || len(got) != 0 {
		t.Fatalf("Suggest with nil provider = %+v; want empty non-nil", got)
	}
}

func TestFunctionSource_NoActiveSession_Empty(t *testing.T) {
	src := NewFunctionSource(func() drivers.Session { return nil })
	got := src.Suggest(context.Background(), nil, Position{})
	if got == nil || len(got) != 0 {
		t.Fatalf("Suggest with nil session = %+v; want empty non-nil", got)
	}
}

func TestFunctionSource_ReturnsNamesWithCalleeDisplay(t *testing.T) {
	sess := &funcFakeSession{names: []string{"now", "lower", "upper"}}
	src := NewFunctionSource(func() drivers.Session { return sess })

	got := src.Suggest(context.Background(), nil, Position{})
	if len(got) != 3 {
		t.Fatalf("len(got) = %d; want 3", len(got))
	}
	for i, want := range []string{"now", "lower", "upper"} {
		if got[i].Text != want {
			t.Errorf("got[%d].Text = %q; want %q", i, got[i].Text, want)
		}
		if got[i].Display != want+"(...)" {
			t.Errorf("got[%d].Display = %q; want %q", i, got[i].Display, want+"(...)")
		}
		if got[i].Source != FunctionSourceName {
			t.Errorf("got[%d].Source = %q; want %q", i, got[i].Source, FunctionSourceName)
		}
	}
}

func TestFunctionSource_CachesAcrossRepeatedCalls(t *testing.T) {
	sess := &funcFakeSession{names: []string{"f1", "f2"}}
	src := NewFunctionSource(func() drivers.Session { return sess })

	for i := range 5 {
		got := src.Suggest(context.Background(), nil, Position{})
		if len(got) != 2 {
			t.Fatalf("iter %d: len(got) = %d; want 2", i, len(got))
		}
	}
	if got := sess.calls.Load(); got != 1 {
		t.Errorf("ListFunctions called %d times; want 1 (cache broken)", got)
	}
}

func TestFunctionSource_CacheInvalidatedOnSessionSwap(t *testing.T) {
	sessA := &funcFakeSession{names: []string{"a_func"}}
	sessB := &funcFakeSession{names: []string{"b_func", "b_other"}}

	var active drivers.Session = sessA
	src := NewFunctionSource(func() drivers.Session { return active })

	// Prime against sessA.
	got := src.Suggest(context.Background(), nil, Position{})
	if len(got) != 1 || got[0].Text != "a_func" {
		t.Fatalf("first Suggest = %+v; want [a_func]", got)
	}
	if sessA.calls.Load() != 1 {
		t.Fatalf("sessA.calls = %d; want 1", sessA.calls.Load())
	}

	// Swap to sessB — cache MUST be discarded and refetched.
	active = sessB
	got = src.Suggest(context.Background(), nil, Position{})
	if len(got) != 2 || got[0].Text != "b_func" {
		t.Fatalf("post-swap Suggest = %+v; want [b_func, b_other]", got)
	}
	if sessB.calls.Load() != 1 {
		t.Fatalf("sessB.calls = %d; want 1", sessB.calls.Load())
	}

	// Re-call: should hit sessB cache, not re-query.
	_ = src.Suggest(context.Background(), nil, Position{})
	if sessB.calls.Load() != 1 {
		t.Fatalf("sessB.calls after re-call = %d; want 1 (cache broken after swap)", sessB.calls.Load())
	}
	// And sessA shouldn't be called again.
	if sessA.calls.Load() != 1 {
		t.Fatalf("sessA.calls after swap = %d; want 1", sessA.calls.Load())
	}
}

func TestFunctionSource_EmptyResultCached(t *testing.T) {
	sess := &funcFakeSession{names: nil}
	src := NewFunctionSource(func() drivers.Session { return sess })

	got := src.Suggest(context.Background(), nil, Position{})
	if got == nil || len(got) != 0 {
		t.Fatalf("first Suggest = %+v; want empty non-nil", got)
	}
	got = src.Suggest(context.Background(), nil, Position{})
	if len(got) != 0 {
		t.Fatalf("second Suggest = %+v; want empty", got)
	}
	if sess.calls.Load() != 1 {
		t.Errorf("ListFunctions called %d times; want 1 (empty result should still cache)", sess.calls.Load())
	}
}

func TestFunctionSource_ErrorReturnsEmptyDoesNotCache(t *testing.T) {
	sess := &funcFakeSession{err: errors.New("boom")}
	src := NewFunctionSource(func() drivers.Session { return sess })

	got := src.Suggest(context.Background(), nil, Position{})
	if got == nil || len(got) != 0 {
		t.Fatalf("Suggest on error = %+v; want empty non-nil", got)
	}
	// A subsequent call should re-attempt (cache not poisoned).
	_ = src.Suggest(context.Background(), nil, Position{})
	if sess.calls.Load() != 2 {
		t.Errorf("ListFunctions called %d times; want 2 (error must not be cached)", sess.calls.Load())
	}
}

func TestFunctionSource_SkipsEmptyNames(t *testing.T) {
	// Defensive: scanner could yield "" if the DB ever returns one;
	// FunctionSource filters them so the popup never shows a blank row.
	sess := &funcFakeSession{names: []string{"ok", "", "ok2"}}
	src := NewFunctionSource(func() drivers.Session { return sess })

	got := src.Suggest(context.Background(), nil, Position{})
	if len(got) != 2 {
		t.Fatalf("len(got) = %d; want 2 (empty name filtered)", len(got))
	}
	if got[0].Text != "ok" || got[1].Text != "ok2" {
		t.Errorf("got = %+v; want [ok, ok2]", got)
	}
}
