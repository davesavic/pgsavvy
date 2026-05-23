package context

import (
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/editor"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

func newTestSuggestions(drv types.GuiDriver) *SuggestionsContext {
	base := NewBaseContext(BaseContextOpts{
		Key:      types.SUGGESTIONS,
		ViewName: string(types.SUGGESTIONS),
		Kind:     types.TEMPORARY_POPUP,
	})
	deps := types.ContextTreeDeps{GuiDriver: drv}
	return NewSuggestionsContext(base, deps)
}

func TestSuggestionsContext_ShowHide_Visibility(t *testing.T) {
	c := newTestSuggestions(nil)
	if c.IsVisible() {
		t.Fatal("zero-value context IsVisible = true; want false")
	}
	c.Show([]editor.Suggestion{{Text: "a", Display: "a"}}, editor.Position{Line: 1, Col: 2})
	if !c.IsVisible() {
		t.Fatal("after Show IsVisible = false; want true")
	}
	c.Hide()
	if c.IsVisible() {
		t.Fatal("after Hide IsVisible = true; want false")
	}
}

func TestSuggestionsContext_Show_EmptyLeavesHidden(t *testing.T) {
	c := newTestSuggestions(nil)
	c.Show(nil, editor.Position{})
	if c.IsVisible() {
		t.Fatal("Show(nil) flipped visible; want hidden")
	}
	c.Show([]editor.Suggestion{}, editor.Position{})
	if c.IsVisible() {
		t.Fatal("Show(empty slice) flipped visible; want hidden")
	}
}

func TestSuggestionsContext_NextPrev_Wrap(t *testing.T) {
	c := newTestSuggestions(nil)
	c.Show([]editor.Suggestion{
		{Text: "a", Display: "a"},
		{Text: "b", Display: "b"},
		{Text: "c", Display: "c"},
	}, editor.Position{})

	if got := c.Selected(); got != 0 {
		t.Fatalf("initial Selected = %d; want 0", got)
	}
	c.Next()
	if got := c.Selected(); got != 1 {
		t.Fatalf("after Next Selected = %d; want 1", got)
	}
	c.Next()
	c.Next() // wraps 2 -> 0
	if got := c.Selected(); got != 0 {
		t.Fatalf("after wrap-around Next Selected = %d; want 0", got)
	}
	c.Prev() // 0 -> 2
	if got := c.Selected(); got != 2 {
		t.Fatalf("after wrap Prev Selected = %d; want 2", got)
	}
}

func TestSuggestionsContext_NextPrev_HiddenNoop(t *testing.T) {
	c := newTestSuggestions(nil)
	c.Next()
	c.Prev()
	if c.IsVisible() {
		t.Fatal("Next/Prev on hidden context should not flip visibility")
	}
	if got := c.Selected(); got != -1 {
		t.Errorf("Selected on hidden = %d; want -1", got)
	}
}

func TestSuggestionsContext_Accept_Visible(t *testing.T) {
	c := newTestSuggestions(nil)
	c.Show([]editor.Suggestion{
		{Text: "first", Display: "first"},
		{Text: "second", Display: "second"},
	}, editor.Position{})
	c.Next() // selected=1
	got, ok := c.Accept()
	if !ok {
		t.Fatal("Accept on visible returned ok=false")
	}
	if got.Text != "second" {
		t.Errorf("Accept Text = %q; want %q", got.Text, "second")
	}
	if c.IsVisible() {
		t.Error("Accept should hide popup; still visible")
	}
}

func TestSuggestionsContext_Accept_HiddenReturnsFalse(t *testing.T) {
	c := newTestSuggestions(nil)
	_, ok := c.Accept()
	if ok {
		t.Error("Accept on hidden returned ok=true; want false")
	}
}

func TestSuggestionsContext_Accept_EmptySuggestionsReturnsFalse(t *testing.T) {
	c := newTestSuggestions(nil)
	// Hand-craft a "visible but empty" state — defensive: Show
	// refuses empty so we go through the field directly.
	c.visible = true
	c.suggestions = nil
	_, ok := c.Accept()
	if ok {
		t.Error("Accept on visible-but-empty returned ok=true; want false")
	}
}

func TestSuggestionsContext_Accept_SanitizesText(t *testing.T) {
	c := newTestSuggestions(nil)
	c.Show([]editor.Suggestion{
		{Text: "se\nle\x00ct", Display: "select"},
	}, editor.Position{})
	got, ok := c.Accept()
	if !ok {
		t.Fatal("Accept returned ok=false")
	}
	if got.Text != "select" {
		t.Errorf("Accept Text = %q; want %q (control + newline stripped)", got.Text, "select")
	}
}

func TestSuggestionsContext_OnCursorMoved_Hides(t *testing.T) {
	c := newTestSuggestions(nil)
	c.Show([]editor.Suggestion{{Text: "a", Display: "a"}}, editor.Position{})
	c.OnCursorMoved()
	if c.IsVisible() {
		t.Error("OnCursorMoved did not hide popup")
	}
}

func TestSuggestionsContext_HandleRender_HiddenNoWrite(t *testing.T) {
	drv := &captureDriver{}
	c := newTestSuggestions(drv)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.writes != 0 {
		t.Errorf("SetContent called %d times when hidden; want 0", drv.writes)
	}
}

func TestSuggestionsContext_HandleRender_VisibleEmitsBody(t *testing.T) {
	drv := &captureDriver{}
	c := newTestSuggestions(drv)
	c.Show([]editor.Suggestion{
		{Text: "select", Display: "select"},
		{Text: "from", Display: "from"},
		{Text: "where", Display: "where"},
	}, editor.Position{})
	c.Next() // selected = 1 ("from")
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.writes != 1 {
		t.Fatalf("writes = %d; want 1", drv.writes)
	}
	if drv.lastView != string(types.SUGGESTIONS) {
		t.Errorf("view = %q; want %q", drv.lastView, string(types.SUGGESTIONS))
	}
	body := drv.lastContent
	for _, want := range []string{"select", "from", "where"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q; got %q", want, body)
		}
	}
	// Marker row must be on the selected ("from") line.
	lines := strings.Split(body, "\n")
	var fromLine, selectLine, whereLine string
	for _, ln := range lines {
		switch {
		case strings.Contains(ln, "from"):
			fromLine = ln
		case strings.Contains(ln, "select"):
			selectLine = ln
		case strings.Contains(ln, "where"):
			whereLine = ln
		}
	}
	if !strings.HasPrefix(fromLine, "> ") {
		t.Errorf("selected line missing '> ' marker; got %q", fromLine)
	}
	if strings.HasPrefix(selectLine, "> ") {
		t.Errorf("unselected select line has marker; got %q", selectLine)
	}
	if strings.HasPrefix(whereLine, "> ") {
		t.Errorf("unselected where line has marker; got %q", whereLine)
	}
}

func TestSuggestionsContext_HandleRender_SanitizesDisplay(t *testing.T) {
	drv := &captureDriver{}
	c := newTestSuggestions(drv)
	c.Show([]editor.Suggestion{
		{Text: "x", Display: "evil\x1b[31mred\x1b[0m"},
	}, editor.Position{})
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if strings.Contains(drv.lastContent, "\x1b") {
		t.Errorf("body contains raw ESC; sanitizer missed it: %q", drv.lastContent)
	}
}

func TestSuggestionsContext_HandleRender_WindowSlides(t *testing.T) {
	drv := &captureDriver{}
	c := newTestSuggestions(drv)
	// 10 entries, visible max 8. Selecting index 9 should slide the
	// window so index 9 is the last visible row.
	items := make([]editor.Suggestion, 10)
	for i := range items {
		items[i] = editor.Suggestion{
			Text:    string(rune('a' + i)),
			Display: string(rune('a' + i)),
		}
	}
	c.Show(items, editor.Position{})
	for i := 0; i < 9; i++ {
		c.Next()
	}
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := drv.lastContent
	if strings.Contains(body, "a") {
		t.Errorf("body still contains 'a' after sliding window: %q", body)
	}
	if !strings.Contains(body, "j") {
		t.Errorf("body missing the last entry 'j': %q", body)
	}
}

func TestSuggestionsContext_HandleRender_NilDriverNoPanic(t *testing.T) {
	c := newTestSuggestions(nil)
	c.Show([]editor.Suggestion{{Text: "a", Display: "a"}}, editor.Position{})
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender with nil driver: %v", err)
	}
}
