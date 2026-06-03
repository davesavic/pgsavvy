package ui

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/session"
)

// promptStub captures the most recent Prompt call and exposes the
// registered submit / cancel callbacks so the test can drive them as the
// PromptController would on <cr> / <esc>.
type promptStub struct {
	mu          sync.Mutex
	label       string
	onSubmit    func(string) error
	onCancel    func() error
	masked      []bool
	active      bool
	cancelCalls int
}

func (s *promptStub) Prompt(label, _ string, onSubmit func(string) error, onCancel func() error) error {
	s.mu.Lock()
	s.label = label
	s.onSubmit = onSubmit
	s.onCancel = onCancel
	s.mu.Unlock()
	return nil
}

func (s *promptStub) SetMasked(on bool) {
	s.mu.Lock()
	s.masked = append(s.masked, on)
	s.mu.Unlock()
}

func (s *promptStub) submit(v string) error {
	s.mu.Lock()
	cb := s.onSubmit
	s.mu.Unlock()
	return cb(v)
}

func (s *promptStub) cancel() error {
	s.mu.Lock()
	cb := s.onCancel
	s.mu.Unlock()
	return cb()
}

func (s *promptStub) waitForCallbacks(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		ready := s.onSubmit != nil && s.onCancel != nil
		s.mu.Unlock()
		if ready {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for prompt callbacks to register")
}

func (s *promptStub) Active() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

func (s *promptStub) Cancel() error {
	s.mu.Lock()
	s.cancelCalls++
	s.mu.Unlock()
	return nil
}

func (s *promptStub) cancelCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancelCalls
}

func (s *promptStub) maskedLog() []bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]bool, len(s.masked))
	copy(out, s.masked)
	return out
}

// inlineScheduler runs the scheduled closure on a fresh goroutine, mimicking
// the gocui MainLoop running the Update closure off the calling worker
// goroutine.
func inlineScheduler(fn func() error) {
	go func() { _ = fn() }()
}

func TestSecretPromptHelper_Submit_ReturnsRealValueMaskedRender(t *testing.T) {
	stub := &promptStub{}
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	h := NewSecretPromptHelper(stub, stub, inlineScheduler)
	h.SetLogger(logger)

	type res struct {
		val string
		err error
	}
	done := make(chan res, 1)
	go func() {
		v, err := h.PromptSecret(context.Background(), "passphrase for id_ed25519")
		done <- res{v, err}
	}()

	stub.waitForCallbacks(t)

	const secret = "hunter2-super-secret"
	if err := stub.submit(secret); err != nil {
		t.Fatalf("submit: %v", err)
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("PromptSecret err = %v; want nil", r.err)
		}
		if r.val != secret {
			t.Fatalf("value = %q, want %q", r.val, secret)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PromptSecret did not return after submit")
	}

	// Masked turned on, then off.
	ml := stub.maskedLog()
	if len(ml) < 2 || ml[0] != true || ml[len(ml)-1] != false {
		t.Errorf("masked log = %v; want first=true last=false", ml)
	}

	// A lifecycle event WAS emitted (so the "no leak" assertion is meaningful)
	// but the secret value never appears in it.
	logged := buf.String()
	if !strings.Contains(logged, "secret_prompt.submit") {
		t.Errorf("expected lifecycle event in log; got %q", logged)
	}
	if strings.Contains(logged, secret) {
		t.Errorf("secret leaked into log output: %q", logged)
	}
}

func TestSecretPromptHelper_Cancel_ReturnsCancelledError(t *testing.T) {
	stub := &promptStub{}
	h := NewSecretPromptHelper(stub, stub, inlineScheduler)

	done := make(chan error, 1)
	go func() {
		_, err := h.PromptSecret(context.Background(), "hint")
		done <- err
	}()

	stub.waitForCallbacks(t)
	if err := stub.cancel(); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	select {
	case err := <-done:
		if !session.IsSecretPromptCancelled(err) {
			t.Fatalf("err = %v; want cancelled", err)
		}
		if session.IsInteractivePromptUnsupported(err) {
			t.Fatal("cancel returned the TUIRefuse refusal sentinel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PromptSecret did not return after cancel")
	}

	ml := stub.maskedLog()
	if len(ml) < 2 || ml[len(ml)-1] != false {
		t.Errorf("masked not cleared on cancel: %v", ml)
	}
}

func TestSecretPromptHelper_EmptySubmit_ReturnsEmptyNilNotError(t *testing.T) {
	stub := &promptStub{}
	h := NewSecretPromptHelper(stub, stub, inlineScheduler)

	done := make(chan error, 1)
	val := make(chan string, 1)
	go func() {
		v, err := h.PromptSecret(context.Background(), "hint")
		val <- v
		done <- err
	}()

	stub.waitForCallbacks(t)
	if err := stub.submit(""); err != nil {
		t.Fatalf("submit empty: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("empty submit err = %v; want nil", err)
		}
		if v := <-val; v != "" {
			t.Fatalf("empty submit value = %q; want \"\"", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PromptSecret did not return after empty submit")
	}
}

func TestSecretPromptHelper_CtxCancel_ReturnsCancelledNoLeak(t *testing.T) {
	stub := &promptStub{}
	h := NewSecretPromptHelper(stub, stub, inlineScheduler)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := h.PromptSecret(ctx, "hint")
		done <- err
	}()

	stub.waitForCallbacks(t)
	cancel()

	select {
	case err := <-done:
		if !session.IsSecretPromptCancelled(err) {
			t.Fatalf("ctx-cancel err = %v; want cancelled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PromptSecret did not return after ctx cancel (goroutine leak)")
	}
}

// TestSecretPromptHelper_MaskedRender_RealView proves the masked-mode render
// path on the concrete PromptContext: with masking on, the rendered content
// buffer shows mask runes, NOT the typed secret. Masking is content-level
// (renderBuffer), so the live gocui View.Mask is deliberately NOT set — setting
// it would mask the label too (dbsavvy-3ye.8). The real secret remains readable
// from the TextArea.
func TestSecretPromptHelper_MaskedRender_RealView(t *testing.T) {
	const secret = "topsecretvalue"

	view := gocui.NewView(string(types.PROMPT), 0, 0, 40, 6, gocui.OutputNormal)
	for _, r := range secret {
		view.TextArea.TypeCharacter(string(r))
	}

	drv := &captureDriver{}
	pc := newPromptContextForMaskTest(drv)
	pc.SetState(&maskTestState{active: true, label: "passphrase"})
	pc.SetView(view)
	pc.SetMasked(true)

	// View.Mask must stay unset: it masks EVERY cell (including the label) at
	// gocui draw time. Masking lives in the content body instead.
	if view.Mask != "" {
		t.Errorf("View.Mask = %q; want unset (it would mask the label too)", view.Mask)
	}

	if err := pc.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := drv.lastContent
	if strings.Contains(body, secret) {
		t.Errorf("secret leaked into rendered content buffer: %q", body)
	}
	if !strings.Contains(body, "•") {
		t.Errorf("masked content missing mask runes: %q", body)
	}

	// Real value still readable from the TextArea.
	if got := view.TextArea.GetContent(); got != secret {
		t.Errorf("TextArea content = %q, want %q", got, secret)
	}

	// Clearing the mask leaves View.Mask unset (never set in the first place).
	pc.SetMasked(false)
	if view.Mask != "" {
		t.Errorf("View.Mask = %q after SetMasked(false); want unset", view.Mask)
	}
}

// TestSecretPromptHelper_MaskedDraw_LabelReadable exercises the actual gocui
// View.Mask-applied DRAW path that TestSecretPromptHelper_MaskedRender_RealView
// (which only inspects the SetContent string, never the drawn cells) missed:
// it renders the masked prompt into a real gocui View attached to a headless
// Gui, draws it to gocui's headless tcell screen, and reads the rendered cells
// back.
// Regression for dbsavvy-3ye.8 — before the fix the code set View.Mask, which
// masks EVERY cell at draw time, so the whole popup (label included) drew as
// bullets. The masked-prompt label now renders as the frame title (the body is
// just the masked input line). Asserts on the DRAWN cells: (1) the label
// renders as readable plaintext (in the title), (2) the typed buffer renders
// only as mask runes, (3) the real secret never reaches the screen.
// Re-introducing View.Mask makes (1) fail.
//
// Must NOT use t.Parallel(): gocui.Screen is a process-level global set by the
// headless Gui. If a second headless-Gui test is ever added to this test binary
// it must be serialized with this one (the two would otherwise clobber each
// other's screen during read-back).
func TestSecretPromptHelper_MaskedDraw_LabelReadable(t *testing.T) {
	const (
		secret = "topsecretvalue"
		label  = "passphrase for SSH key"
	)

	g, err := gocui.NewGui(gocui.NewGuiOpts{OutputMode: gocui.OutputNormal, Headless: true, Width: 60, Height: 10})
	if err != nil {
		t.Fatalf("headless NewGui: %v", err)
	}
	defer g.Close()

	pc := newPromptContextForMaskTest(&headlessDriver{g: g})
	pc.SetState(&maskTestState{active: true, label: label})
	pc.SetMasked(true)

	typed := false
	g.SetManagerFunc(func(g *gocui.Gui) error {
		v, err := g.SetView(string(types.PROMPT), 0, 0, 58, 8, 0)
		if err != nil && !errors.Is(err, gocui.ErrUnknownView) {
			return err
		}
		v.Editable = true
		v.Wrap = true
		if !typed {
			for _, r := range secret {
				v.TextArea.TypeCharacter(string(r))
			}
			typed = true
		}
		// Mirror the orchestrator's layout pass: plumb the live view, set the
		// wrap width, publish the masked-prompt label as the frame title, then
		// render. SetMasked was called above so a regressed SetView/SetMasked
		// that re-applies View.Mask would mask the title too and trip here.
		pc.SetView(v)
		pc.SetLabelWrapWidth(v.InnerWidth())
		v.Title = pc.GetTitle()
		return pc.HandleRender()
	})

	if err := g.ForceLayoutAndRedraw(); err != nil {
		t.Fatalf("ForceLayoutAndRedraw: %v", err)
	}

	drawn := readSimScreen(t)
	if !strings.Contains(drawn, label) {
		t.Errorf("label not readable in drawn cells (masked-label regression):\n%s", drawn)
	}
	if strings.Contains(drawn, secret) {
		t.Errorf("secret leaked into drawn cells:\n%s", drawn)
	}
	if !strings.Contains(drawn, secretMaskRune) {
		t.Errorf("typed input not masked in drawn cells:\n%s", drawn)
	}
}

// secretMaskRune mirrors the unexported constant in pkg/gui/context; the test
// asserts the drawn cells contain it (masked input) and the plaintext label.
const secretMaskRune = "•"

// headlessDriver is a GuiDriver that targets a headless gocui.Gui's views by
// name so PromptContext.HandleRender writes into the real (drawable) view.
type headlessDriver struct {
	types.GuiDriver
	g *gocui.Gui
}

func (d *headlessDriver) SetContent(viewName, str string) error {
	v, err := d.g.View(viewName)
	if err != nil {
		return err
	}
	v.SetContent(str)
	return nil
}

func (d *headlessDriver) Update(fn func() error) {
	if fn != nil {
		_ = fn()
	}
}

// readSimScreen flattens the headless gocui screen's drawn cell buffer into
// newline-joined rows so tests can assert on what the terminal would show.
// gocui draws via tcell Screen.Put; Screen.Get reads the same CellBuffer.
func readSimScreen(t *testing.T) string {
	t.Helper()
	scr := gocui.Screen
	if scr == nil {
		t.Fatal("gocui.Screen is nil after headless NewGui")
	}
	w, h := scr.Size()
	var b strings.Builder
	for y := range h {
		for x := range w {
			str, _, _ := scr.Get(x, y)
			if str == "" || str == "\x00" {
				b.WriteByte(' ')
				continue
			}
			b.WriteString(str)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// maskTestState is a minimal PromptState for the render test.
type maskTestState struct {
	active bool
	label  string
}

func (m *maskTestState) Active() bool  { return m.active }
func (m *maskTestState) Label() string { return m.label }

func newPromptContextForMaskTest(drv types.GuiDriver) *guicontext.PromptContext {
	base := guicontext.NewBaseContext(guicontext.BaseContextOpts{
		Key:      types.PROMPT,
		ViewName: string(types.PROMPT),
		Kind:     types.TEMPORARY_POPUP,
	})
	return guicontext.NewPromptContext(base, types.ContextTreeDeps{GuiDriver: drv})
}

// captureDriver is a no-op GuiDriver that records the last SetContent.
type captureDriver struct {
	types.GuiDriver
	lastContent string
}

func (d *captureDriver) SetContent(_ string, str string) error {
	d.lastContent = str
	return nil
}

func (d *captureDriver) Update(fn func() error) {
	if fn != nil {
		_ = fn()
	}
}

// TestSecretPromptHelper_CtxCancel_DismissesActivePopup proves a ctx-cancel
// while the popup is still active clears the mask AND dismisses the popup,
// leaving no orphaned masked prompt on screen.
func TestSecretPromptHelper_CtxCancel_DismissesActivePopup(t *testing.T) {
	stub := &promptStub{active: true}
	h := NewSecretPromptHelper(stub, stub, inlineScheduler)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := h.PromptSecret(ctx, "hint")
		done <- err
	}()

	stub.waitForCallbacks(t)
	cancel()

	select {
	case err := <-done:
		if !session.IsSecretPromptCancelled(err) {
			t.Fatalf("ctx-cancel err = %v; want cancelled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PromptSecret did not return after ctx cancel")
	}

	// The dismissal runs on the inlineScheduler goroutine, so poll for it.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if stub.cancelCount() == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if got := stub.cancelCount(); got != 1 {
		t.Fatalf("cancelCalls = %d; want 1 (popup not dismissed)", got)
	}

	ml := stub.maskedLog()
	if len(ml) == 0 || ml[len(ml)-1] != false {
		t.Errorf("masked not cleared on ctx-cancel: %v", ml)
	}
}

// TestSecretPromptHelper_Overlapping_ReturnsBusy proves a second PromptSecret
// while one is already in flight returns the busy sentinel rather than
// clobbering the single shared popup surface.
func TestSecretPromptHelper_Overlapping_ReturnsBusy(t *testing.T) {
	stub := &promptStub{}
	h := NewSecretPromptHelper(stub, stub, inlineScheduler)

	done := make(chan error, 1)
	go func() {
		_, err := h.PromptSecret(context.Background(), "hint")
		done <- err
	}()

	// Guarantees call #1 has set busy and registered its callbacks.
	stub.waitForCallbacks(t)

	if _, err := h.PromptSecret(context.Background(), "hint2"); !session.IsSecretPromptBusy(err) {
		t.Fatalf("second PromptSecret err = %v; want busy", err)
	}

	// Drain call #1 so it finishes cleanly (no goroutine leak).
	if err := stub.submit("value"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("call #1 err = %v; want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("call #1 did not return after submit")
	}
}
