package popup

import (
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

type fakePanel struct {
	body string
	keys []types.Key
}

func (f *fakePanel) Body() string { return f.body }

func (f *fakePanel) HandleKey(k types.Key) bool {
	f.keys = append(f.keys, k)
	return false
}

func twoTabs() []Tab {
	return []Tab{
		{Title: "Alpha", Panel: &fakePanel{body: "alpha-body"}},
		{Title: "Beta", Panel: &fakePanel{body: "beta-body"}},
	}
}

func TestNewTabbedPopup_InitialActive(t *testing.T) {
	p := NewTabbedPopup(twoTabs())
	if got := p.Active(); got != 0 {
		t.Fatalf("Active() = %d, want 0", got)
	}
}

func TestNextTab_Wraps(t *testing.T) {
	p := NewTabbedPopup(twoTabs())
	p.NextTab()
	if got := p.Active(); got != 1 {
		t.Fatalf("after NextTab Active() = %d, want 1", got)
	}
	p.NextTab()
	if got := p.Active(); got != 0 {
		t.Fatalf("after NextTab wrap Active() = %d, want 0", got)
	}
}

func TestPrevTab_Wraps(t *testing.T) {
	p := NewTabbedPopup(twoTabs())
	p.PrevTab()
	if got := p.Active(); got != 1 {
		t.Fatalf("after PrevTab wrap Active() = %d, want 1", got)
	}
	p.PrevTab()
	if got := p.Active(); got != 0 {
		t.Fatalf("after PrevTab Active() = %d, want 0", got)
	}
}

func TestSetActive_Clamps(t *testing.T) {
	p := NewTabbedPopup(twoTabs())
	p.SetActive(1)
	if got := p.Active(); got != 1 {
		t.Fatalf("SetActive(1) Active() = %d, want 1", got)
	}
	p.SetActive(-1)
	if got := p.Active(); got != 1 {
		t.Fatalf("SetActive(-1) changed Active to %d, want 1 (no-op)", got)
	}
	p.SetActive(99)
	if got := p.Active(); got != 1 {
		t.Fatalf("SetActive(99) changed Active to %d, want 1 (no-op)", got)
	}
}

func TestEmptyTabs_NoOp(t *testing.T) {
	p := NewTabbedPopup(nil)
	if got := p.Active(); got != 0 {
		t.Fatalf("empty Active() = %d, want 0", got)
	}
	if got := p.Body(); got != "" {
		t.Fatalf("empty Body() = %q, want \"\"", got)
	}
	// Must not panic.
	p.NextTab()
	p.PrevTab()
	p.SetActive(0)
	p.SetActive(-1)
	if got := p.Active(); got != 0 {
		t.Fatalf("after no-op ops Active() = %d, want 0", got)
	}
}

func TestBody_ActiveTitleANSI_ColorMode(t *testing.T) {
	p := newTabbedPopupWithMono(twoTabs(), func() bool { return false })
	out := p.Body()

	want := "\x1b[33mAlpha\x1b[0m"
	if !strings.Contains(out, want) {
		t.Fatalf("Body() missing active-title ANSI %q; got:\n%s", want, out)
	}
	// Inactive title must NOT be preceded by the yellow escape.
	if strings.Contains(out, "\x1b[33mBeta") {
		t.Fatalf("Body() colored inactive title; got:\n%s", out)
	}
	// Inactive title must still appear plain.
	if !strings.Contains(out, "Beta") {
		t.Fatalf("Body() missing inactive title; got:\n%s", out)
	}
}

func TestBody_ActiveTitleBracketed_MonochromeMode(t *testing.T) {
	p := newTabbedPopupWithMono(twoTabs(), func() bool { return true })
	out := p.Body()

	if !strings.Contains(out, "[Alpha]") {
		t.Fatalf("Body() missing bracketed active title; got:\n%s", out)
	}
	if strings.ContainsRune(out, '\x1b') {
		t.Fatalf("Body() contains ESC byte under monochrome mode; got:\n%s", out)
	}
}

func TestBody_DelegatesToActivePanel(t *testing.T) {
	const sentinel = "SENTINEL-PANEL-BODY-7f3e"
	tabs := []Tab{
		{Title: "Alpha", Panel: &fakePanel{body: sentinel}},
		{Title: "Beta", Panel: &fakePanel{body: "other"}},
	}
	p := newTabbedPopupWithMono(tabs, func() bool { return true })
	out := p.Body()
	if !strings.Contains(out, sentinel) {
		t.Fatalf("Body() did not contain active panel sentinel; got:\n%s", out)
	}

	p.NextTab()
	out2 := p.Body()
	if !strings.Contains(out2, "other") {
		t.Fatalf("Body() after NextTab missing new panel body; got:\n%s", out2)
	}
	if strings.Contains(out2, sentinel) {
		t.Fatalf("Body() after NextTab still showed old panel body; got:\n%s", out2)
	}
}

func TestHandleKey_NotCalledByPopup(t *testing.T) {
	fp0 := &fakePanel{body: "a"}
	fp1 := &fakePanel{body: "b"}
	tabs := []Tab{
		{Title: "Alpha", Panel: fp0},
		{Title: "Beta", Panel: fp1},
	}
	p := newTabbedPopupWithMono(tabs, func() bool { return true })

	p.NextTab()
	p.PrevTab()
	p.SetActive(1)
	p.SetActive(0)
	_ = p.Body()
	_ = p.Body()

	if len(fp0.keys) != 0 {
		t.Fatalf("tab 0 panel.HandleKey called %d times; want 0", len(fp0.keys))
	}
	if len(fp1.keys) != 0 {
		t.Fatalf("tab 1 panel.HandleKey called %d times; want 0", len(fp1.keys))
	}
}
