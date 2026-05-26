package keys

import (
	"errors"
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/jesseduffield/lazygit/pkg/gocui"
)

// TestMatcher_HandlerError_ToastAndSwallowed proves the central error
// boundary (bd dbsavvy-9v1.4): a leaf handler that returns a non-nil
// error keeps the app alive (Dispatch returns nil err — nothing reaches
// gocui's MainLoop) and surfaces the error as a sanitized toast.
func TestMatcher_HandlerError_ToastAndSwallowed(t *testing.T) {
	cmd := &commands.Command{
		ID:          "demo.boom",
		Description: "Demo Boom",
		// Embed an ANSI escape so the sanitization assertion has teeth.
		Handler: func(commands.ExecCtx) error {
			return errors.New("connect failed\x1b[2J")
		},
	}
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeNormal, types.QUERY_EDITOR, []Key{keyOf('x')}, cmd},
	})
	spy := &recordingToastSpy{}
	m := shortMatcherWithToaster(t, ts, types.QUERY_EDITOR, types.ModeNormal, spy.toast)

	res, err := m.Dispatch(types.QUERY_EDITOR, keyOf('x'))
	if err != nil {
		t.Fatalf("Dispatch returned err = %v; the boundary must swallow handler errors so they never reach gocui MainLoop", err)
	}
	if res != Dispatched {
		t.Errorf("res = %v, want Dispatched", res)
	}

	msgs := spy.snapshot()
	if len(msgs) != 1 {
		t.Fatalf("toast count = %d (%v), want 1", len(msgs), msgs)
	}
	if !strings.Contains(msgs[0], "connect failed") {
		t.Errorf("toast %q does not surface the handler error text", msgs[0])
	}
	if strings.Contains(msgs[0], "\x1b") {
		t.Errorf("toast %q leaked a raw ESC byte; must be sanitized via SafeText", msgs[0])
	}
}

// TestMatcher_HandlerErrQuit_Propagates guards the control-flow carve
// out: gocui.ErrQuit (the :q ex-command / quit path) must escape the
// boundary so the main loop can unwind, and must NOT be toasted.
func TestMatcher_HandlerErrQuit_Propagates(t *testing.T) {
	cmd := &commands.Command{
		ID:      "demo.quit",
		Handler: func(commands.ExecCtx) error { return gocui.ErrQuit },
	}
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeNormal, types.QUERY_EDITOR, []Key{keyOf('Z')}, cmd},
	})
	spy := &recordingToastSpy{}
	m := shortMatcherWithToaster(t, ts, types.QUERY_EDITOR, types.ModeNormal, spy.toast)

	_, err := m.Dispatch(types.QUERY_EDITOR, keyOf('Z'))
	if !errors.Is(err, gocui.ErrQuit) {
		t.Fatalf("Dispatch err = %v; gocui.ErrQuit must propagate so the quit path works", err)
	}
	if msgs := spy.snapshot(); len(msgs) != 0 {
		t.Errorf("ErrQuit was toasted (%v); quit must not surface a toast", msgs)
	}
}
