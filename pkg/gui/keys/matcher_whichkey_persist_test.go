package keys

import (
	"sync"
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// A pure-prefix partial in a non-insert mode is a which-key
// waypoint. It MUST stay pending (so the popup stays visible) until the
// next key or <esc> — it must NOT be abandoned by the inactivity timer
// after timeout_len. Before the fix, onTimerFire dropped the pending in
// normal mode, killing the popup ~(timeout_len - whichkey_delay) after the
// prefix keypress.
func TestMatcher_PurePrefixNormalMode_PersistsPastTimeout(t *testing.T) {
	var fired []string
	var mu sync.Mutex
	ggTop := recordingCmd("buffer.top", &fired, &mu, nil)
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeNormal, types.QUERY_EDITOR, []Key{keyOf('g'), keyOf('g')}, ggTop},
	})
	m := shortMatcher(t, ts, types.QUERY_EDITOR, types.ModeNormal) // TimeoutLen 30ms

	if res, _ := m.Dispatch(types.QUERY_EDITOR, keyOf('g')); res != Pending {
		t.Fatalf("Dispatch g: want Pending")
	}

	// Wait well past tlen. Old behaviour abandons the pure prefix here.
	time.Sleep(80 * time.Millisecond)

	if !m.IsPartial() {
		t.Fatalf("IsPartial after >tlen = false; pure prefix was abandoned (regression)")
	}

	// The buffered prefix still completes the chord.
	res, err := m.Dispatch(types.QUERY_EDITOR, keyOf('g'))
	if err != nil {
		t.Fatalf("Dispatch g(2): %v", err)
	}
	if res != Dispatched {
		t.Errorf("res = %v, want Dispatched (chord survived timeout)", res)
	}
	mu.Lock()
	got := append([]string(nil), fired...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "buffer.top" {
		t.Errorf("fired = %v, want [buffer.top]", got)
	}
}
