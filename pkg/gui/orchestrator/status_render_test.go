package orchestrator

import (
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui"
	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/i18n"
)

// fakeToastSource implements ToastSource with explicit message/level
// pairs so the unit tests don't have to wire the full helper.
type fakeToastSource struct {
	msg   string
	level ui.ToastLevel
}

func (f fakeToastSource) Current() string             { return f.msg }
func (f fakeToastSource) CurrentLevel() ui.ToastLevel { return f.level }

// newStatusRenderRecorder builds a recorder driver with the
// AppStatusViewName view pre-created so SetContent writes land in the
// recorder's view buffer (and not as ErrUnknownView).
func newStatusRenderRecorder(t *testing.T) *testfake.RecorderGuiDriver {
	t.Helper()
	rec := testfake.NewRecorderGuiDriver()
	// SetView for a fresh view returns gocui.ErrUnknownView as the
	// "created" sentinel; the recorder still allocates the buffer,
	// which is all we need for SetContent to land. Ignore the
	// "created" sentinel here.
	_, _ = rec.SetView(AppStatusViewName, 0, 0, 80, 1, 0)
	return rec
}

// TestRenderStatusLine_ToastActiveWritesSafeTextToStatusView covers
// AC #1 (toast text painted into AppStatusViewName cells while
// Current() is non-empty) and AC #4 (SafeText applied before
// SetContent).
func TestRenderStatusLine_ToastActiveWritesSafeTextToStatusView(t *testing.T) {
	rec := newStatusRenderRecorder(t)
	RenderStatusLine(StatusRenderDeps{
		Driver: rec,
		Toast:  fakeToastSource{msg: "config reloaded", level: ui.ToastInfo},
	})
	buf := rec.GetViewBuffer(AppStatusViewName)
	if !strings.Contains(buf, "config reloaded") {
		t.Fatalf("AppStatusViewName buffer = %q; want it to contain toast text", buf)
	}
}

// TestRenderStatusLine_ToastSafeTextStripsControlBytes covers AC #4:
// a toast carrying a control byte (e.g. "\x1bX") must be sanitised
// before reaching SetContent. The ANSI wrapper added by status_render
// is the only \x1b that may appear in the final buffer; the user-
// supplied \x1b must be gone.
func TestRenderStatusLine_ToastSafeTextStripsControlBytes(t *testing.T) {
	rec := newStatusRenderRecorder(t)
	RenderStatusLine(StatusRenderDeps{
		Driver: rec,
		Toast:  fakeToastSource{msg: "evil\x1bXtail", level: ui.ToastInfo},
	})
	buf := rec.GetViewBuffer(AppStatusViewName)
	// The 'X' (the printable tail of "\x1bX") must survive because
	// SafeText is minimal-loss; the \x1b itself must be stripped from
	// the user portion. Confirm by checking the user substring is
	// "evilXtail" (no \x1b between "evil" and "X").
	if !strings.Contains(buf, "evilXtail") {
		t.Fatalf("expected sanitised 'evilXtail' in buffer; got %q", buf)
	}
	// Strip the ANSI wrapper we added and confirm no \x1b survives in
	// the inner payload — i.e. the user's \x1b was removed.
	inner := strings.TrimPrefix(buf, ansiGreenFgSGR)
	inner = strings.TrimSuffix(inner, ansiResetSGR)
	if strings.ContainsRune(inner, 0x1b) {
		t.Fatalf("user-supplied \\x1b leaked into cell payload: %q", inner)
	}
}

// TestRenderStatusLine_InfoVsErrorDistinguishableStyle covers AC #3:
// success and error toasts produce distinguishable foreground style
// attributes — at the cell-content level, NOT just at the message text
// level. The ANSI SGR codes are the observable bytes.
func TestRenderStatusLine_InfoVsErrorDistinguishableStyle(t *testing.T) {
	for _, tc := range []struct {
		name  string
		level ui.ToastLevel
		want  string
	}{
		{"info_green", ui.ToastInfo, ansiGreenFgSGR},
		{"error_red", ui.ToastError, ansiRedFgSGR},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := newStatusRenderRecorder(t)
			RenderStatusLine(StatusRenderDeps{
				Driver: rec,
				Toast:  fakeToastSource{msg: "msg", level: tc.level},
			})
			buf := rec.GetViewBuffer(AppStatusViewName)
			if !strings.Contains(buf, tc.want) {
				t.Fatalf("buffer for %s = %q; want SGR prefix %q", tc.name, buf, tc.want)
			}
		})
	}
	// Cross-check: error buffer must NOT contain the success SGR (and
	// vice versa) — the two paint distinguishable foreground bytes.
	recInfo := newStatusRenderRecorder(t)
	recErr := newStatusRenderRecorder(t)
	RenderStatusLine(StatusRenderDeps{
		Driver: recInfo,
		Toast:  fakeToastSource{msg: "ok", level: ui.ToastInfo},
	})
	RenderStatusLine(StatusRenderDeps{
		Driver: recErr,
		Toast:  fakeToastSource{msg: "ok", level: ui.ToastError},
	})
	if strings.Contains(recInfo.GetViewBuffer(AppStatusViewName), ansiRedFgSGR) {
		t.Fatalf("info buffer contained error SGR")
	}
	if strings.Contains(recErr.GetViewBuffer(AppStatusViewName), ansiGreenFgSGR) {
		t.Fatalf("error buffer contained info SGR")
	}
}

// TestRenderStatusLine_ToastExpiryFallsThroughToDefaultBranch covers
// AC #2: once Current() returns "" (toast expired), the multiplex
// branch must NOT paint the toast — the renderer falls through to the
// default-line branch. We verify the falling-through observable by
// proving that with an empty toast AND no runtime, the renderer
// returns before issuing any SetContent (so the buffer stays at its
// pre-call state). The "default text overwrites" leg is then covered
// by the integration smoke test which wires the full runtime.
func TestRenderStatusLine_ToastExpiryFallsThroughToDefaultBranch(t *testing.T) {
	rec := newStatusRenderRecorder(t)

	// Pre-seed the buffer with a sentinel so we can detect any
	// SetContent call performed by the renderer.
	const sentinel = "PRE_EXISTING_CONTENT"
	if err := rec.SetContent(AppStatusViewName, sentinel); err != nil {
		t.Fatalf("seed SetContent: %v", err)
	}

	// Expired toast — Current() returns "" so the multiplex branch
	// must fall through. Without runtime/tree the renderer returns
	// before any further SetContent — proving the toast branch did
	// not paint stale content.
	RenderStatusLine(StatusRenderDeps{
		Driver: rec,
		Toast:  fakeToastSource{msg: "", level: ui.ToastInfo},
	})
	if got := rec.GetViewBuffer(AppStatusViewName); got != sentinel {
		t.Fatalf("buffer after expired-toast render = %q; want sentinel %q (multiplex branch must have fallen through, default branch must not run without runtime)", got, sentinel)
	}
}

// TestRenderStatusLine_NoToastBaseline covers AC #5 (no-toast baseline
// regression). With Toast==nil the renderer must produce identical
// cell content to the pre-multiplex behaviour — i.e. it falls through
// to the default-line branch and writes nothing here (no KbRuntime).
func TestRenderStatusLine_NoToastBaseline(t *testing.T) {
	rec := newStatusRenderRecorder(t)
	RenderStatusLine(StatusRenderDeps{
		Driver: rec,
		Toast:  nil,
	})
	buf := rec.GetViewBuffer(AppStatusViewName)
	// Defensive bootstrap-order guard returns before any SetContent.
	// The view exists but its buffer must be untouched (empty).
	if buf != "" {
		t.Fatalf("baseline (no toast, no runtime) buffer = %q; want empty (default branch returns early)", buf)
	}
	// Same flow but with an empty Toast source — must behave the same.
	rec2 := newStatusRenderRecorder(t)
	RenderStatusLine(StatusRenderDeps{
		Driver: rec2,
		Toast:  fakeToastSource{msg: "", level: ui.ToastInfo},
	})
	if rec2.GetViewBuffer(AppStatusViewName) != "" {
		t.Fatalf("empty-toast buffer = %q; want empty", rec2.GetViewBuffer(AppStatusViewName))
	}
}

// TestRenderStatusLine_RapidSuccessiveLatestWins covers the edge-case
// AC: rapid successive toasts — the latest one is what's visible. The
// helper itself implements latest-wins on Show; this test exercises
// that the renderer simply reflects Current() each call (no internal
// queueing in status_render).
func TestRenderStatusLine_RapidSuccessiveLatestWins(t *testing.T) {
	rec := newStatusRenderRecorder(t)
	h := ui.NewToastHelper(nil)
	h.Show("first message", 0)
	h.Show("second message", 0)
	h.Show("third message", 0)

	RenderStatusLine(StatusRenderDeps{
		Driver: rec,
		Toast:  h,
	})
	buf := rec.GetViewBuffer(AppStatusViewName)
	if !strings.Contains(buf, "third message") {
		t.Fatalf("buffer = %q; want latest 'third message'", buf)
	}
	if strings.Contains(buf, "first message") || strings.Contains(buf, "second message") {
		t.Fatalf("buffer leaked an earlier toast: %q", buf)
	}
}

// TestToastHelper_ClassifyLevelByMessageContent covers the heuristic
// classification at the helper boundary — emission sites pass only a
// string, so the helper derives the level from message content.
func TestToastHelper_ClassifyLevelByMessageContent(t *testing.T) {
	for _, tc := range []struct {
		msg  string
		want ui.ToastLevel
	}{
		{"config reloaded", ui.ToastInfo},
		{"reload superseded", ui.ToastInfo},
		{"reload failed: bad yaml", ui.ToastError},
		{"build panic: nil deref", ui.ToastError},
		{"error: unknown command", ui.ToastError},
		// "unknown ex-command: X" is emitted by pkg/gui/keys/command_line.go
		// for the unknown-ex-command path. Without "unknown" in the
		// classifier substring list this string would be Info-classified
		// (green) — the heuristic must paint it red.
		{"unknown ex-command: bogus", ui.ToastError},
	} {
		t.Run(tc.msg, func(t *testing.T) {
			h := ui.NewToastHelper(nil)
			h.Show(tc.msg, 0)
			if got := h.CurrentLevel(); got != tc.want {
				t.Fatalf("CurrentLevel() = %v; want %v for msg %q", got, tc.want, tc.msg)
			}
		})
	}
}

// renderWithSearchProvider drives RenderStatusLine through its default
// (non-toast) branch with a minimal Tree + KbRuntime so the search
// segment append path executes. focusKey is the key of the pushed
// context (use a result-tab key to simulate result-pane focus);
// searchStatus is injected as the SearchStatus provider. Returns the
// resulting AppStatusViewName buffer.
func renderWithSearchProvider(t *testing.T, focusKey types.ContextKey, searchStatus func() (string, int, int, bool)) string {
	t.Helper()
	rec := newStatusRenderRecorder(t)

	tree := gui.NewContextTree()
	if err := tree.Push(guicontext.NewStubContext(focusKey, string(focusKey))); err != nil {
		t.Fatalf("push stub context: %v", err)
	}
	rt := keys.NewRuntime(nil, nil, keys.NewModeStore(), nil, nil)

	RenderStatusLine(StatusRenderDeps{
		Driver:       rec,
		Tree:         tree,
		KbRuntime:    rt,
		Tr:           i18n.EnglishTranslationSet(),
		SearchStatus: searchStatus,
	})
	return rec.GetViewBuffer(AppStatusViewName)
}

// TestRenderStatusLine_SearchProviderConsultedAndRendered proves the
// SearchStatus provider seam is consulted on the render pass and its
// active output appears in the status line (dbsavvy-2ttm.5).
func TestRenderStatusLine_SearchProviderConsultedAndRendered(t *testing.T) {
	consulted := false
	buf := renderWithSearchProvider(t, types.ResultTabKey(0), func() (string, int, int, bool) {
		consulted = true
		return "alic", 3, 40, true
	})
	if !consulted {
		t.Fatalf("SearchStatus provider was not consulted")
	}
	if !strings.Contains(buf, "search: alic 3/40") {
		t.Fatalf("status buffer = %q; want it to contain active search segment", buf)
	}
}

// TestRenderStatusLine_SearchSegmentAbsentWhenInactive covers the
// clear-on-inactive / clear-on-tab-switch mechanism: when the live
// provider reports active=false (focus left a result tab or search was
// cleared) the segment must be absent on the next frame.
func TestRenderStatusLine_SearchSegmentAbsentWhenInactive(t *testing.T) {
	buf := renderWithSearchProvider(t, types.ResultTabKey(0), func() (string, int, int, bool) {
		return "", 0, 0, false
	})
	if strings.Contains(buf, "search:") {
		t.Fatalf("status buffer = %q; want no search segment when provider inactive", buf)
	}
}

// TestRenderStatusLine_SearchSegmentAbsentWhenProviderNil confirms the
// bootstrap-safety fallback: a nil SearchStatus provider renders no
// segment and does not panic.
func TestRenderStatusLine_SearchSegmentAbsentWhenProviderNil(t *testing.T) {
	buf := renderWithSearchProvider(t, types.ResultTabKey(0), nil)
	if strings.Contains(buf, "search:") {
		t.Fatalf("status buffer = %q; want no search segment when provider nil", buf)
	}
}
