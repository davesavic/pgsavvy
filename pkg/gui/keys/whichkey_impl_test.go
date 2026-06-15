package keys

import (
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// whichkey tests live in the same package so seqForTest is reachable.

const wkDelay = 20 * time.Millisecond

func waitForVisible(t *testing.T, w *WhichKey, want bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if w.Visible() == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("waitForVisible timeout: got %v, want %v: %s", w.Visible(), want, msg)
}

func TestWhichKey_NewIsNotVisible(t *testing.T) {
	w := NewWhichKey()
	if w.Visible() {
		t.Errorf("Visible() = true on fresh WhichKey, want false")
	}
	scope, prefix, vis := w.Snapshot()
	if scope != "" || prefix != nil || vis {
		t.Errorf("Snapshot on fresh = (%q, %v, %v); want (\"\", nil, false)", scope, prefix, vis)
	}
}

func TestWhichKey_TimerFireFlipsVisible(t *testing.T) {
	w := NewWhichKey()
	w.ShowAfter(wkDelay, types.GLOBAL, []Key{{Code: 'g'}})
	if w.Visible() {
		t.Errorf("Visible() = true immediately after ShowAfter; want false until timer fires")
	}
	waitForVisible(t, w, true, "timer should fire and flip Visible")
}

func TestWhichKey_HideCancelsBeforeFire(t *testing.T) {
	w := NewWhichKey()
	w.ShowAfter(wkDelay, types.GLOBAL, []Key{{Code: 'g'}})
	w.Hide()
	// Wait past the original delay; if the timer fires it must NOT flip visible.
	time.Sleep(wkDelay + 30*time.Millisecond)
	if w.Visible() {
		t.Fatalf("Visible() = true after Hide cancelled the schedule")
	}
}

func TestWhichKey_SecondShowAfterCancelsFirst(t *testing.T) {
	w := NewWhichKey()
	w.ShowAfter(wkDelay, types.GLOBAL, []Key{{Code: 'g'}})
	seqAfter1 := w.seqForTest()
	w.ShowAfter(wkDelay, types.SCHEMAS, []Key{{Code: 'd'}})
	seqAfter2 := w.seqForTest()
	if seqAfter2 == seqAfter1 {
		t.Fatalf("seq did not advance on second ShowAfter")
	}
	waitForVisible(t, w, true, "second schedule should fire")
	scope, prefix, vis := w.Snapshot()
	if !vis {
		t.Fatalf("Snapshot.vis = false after timer fire")
	}
	if scope != types.SCHEMAS {
		t.Errorf("scope = %q, want CONNECTIONS (second wins)", scope)
	}
	if len(prefix) != 1 || prefix[0].Code != 'd' {
		t.Errorf("prefix = %+v, want [d]", prefix)
	}
}

func TestWhichKey_HideIsIdempotent(t *testing.T) {
	w := NewWhichKey()
	w.Hide()
	w.Hide()
	if w.Visible() {
		t.Errorf("Visible() = true after double Hide on fresh helper")
	}
	w.ShowAfter(wkDelay, types.GLOBAL, []Key{{Code: 'q'}})
	w.Hide()
	w.Hide()
	if w.Visible() {
		t.Errorf("Visible() = true after Show+Hide+Hide")
	}
}

func TestWhichKey_SnapshotReturnsFreshPrefixCopy(t *testing.T) {
	w := NewWhichKey()
	w.ShowAfter(0, types.GLOBAL, []Key{{Code: 'a'}, {Code: 'b'}}) // immediate
	_, prefix, vis := w.Snapshot()
	if !vis {
		t.Fatalf("vis = false after immediate ShowAfter(0)")
	}
	if len(prefix) != 2 {
		t.Fatalf("prefix len = %d, want 2", len(prefix))
	}
	// Mutate the returned slice; internal state must be unaffected.
	prefix[0] = Key{Code: 'X'}
	_, again, _ := w.Snapshot()
	if again[0].Code != 'a' {
		t.Fatalf("internal prefix was mutated through Snapshot: got %+v", again)
	}
}

func TestWhichKey_ShowAfterCopiesCallerPrefix(t *testing.T) {
	w := NewWhichKey()
	in := []Key{{Code: 'g'}}
	w.ShowAfter(0, types.GLOBAL, in)
	// Caller mutates the slice it handed in; WhichKey must hold a copy.
	in[0] = Key{Code: 'X'}
	_, prefix, _ := w.Snapshot()
	if prefix[0].Code != 'g' {
		t.Fatalf("WhichKey did not copy caller prefix; got %+v after caller mutation", prefix)
	}
}

func TestWhichKey_SnapshotBeforeFire_PrefixPresentVisibleFalse(t *testing.T) {
	w := NewWhichKey()
	w.ShowAfter(wkDelay, types.GLOBAL, []Key{{Code: 'g'}})
	scope, prefix, vis := w.Snapshot()
	if vis {
		t.Fatalf("vis = true before timer fired")
	}
	if scope != types.GLOBAL {
		t.Errorf("scope = %q, want GLOBAL", scope)
	}
	if len(prefix) != 1 || prefix[0].Code != 'g' {
		t.Errorf("prefix = %+v, want [g]", prefix)
	}
}

func TestWhichKey_StaleTimerNoOpAfterHide(t *testing.T) {
	w := NewWhichKey()
	w.ShowAfter(wkDelay, types.GLOBAL, []Key{{Code: 'g'}})
	seqBefore := w.seqForTest()
	w.Hide()
	seqAfter := w.seqForTest()
	if seqAfter == seqBefore {
		t.Fatalf("seq did not advance on Hide")
	}
	// Even if the AfterFunc closure happens to race the Hide and grab
	// the mutex after, the id check prevents it from flipping anything.
	time.Sleep(wkDelay + 30*time.Millisecond)
	if w.Visible() {
		t.Fatalf("stale timer flipped visible after Hide")
	}
}

func TestWhichKey_SatisfiesNotifierInterface(t *testing.T) {
	var _ WhichKeyNotifier = NewWhichKey()
}

func TestWhichKey_SatisfiesStateInterface(t *testing.T) {
	var _ types.WhichKeyState = NewWhichKey()
}
