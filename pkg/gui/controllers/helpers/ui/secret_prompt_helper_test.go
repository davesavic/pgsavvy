package ui

import (
	"bytes"
	"context"
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
	mu       sync.Mutex
	label    string
	onSubmit func(string) error
	onCancel func() error
	masked   []bool
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
// buffer shows mask runes, NOT the typed secret, and the live gocui View.Mask
// is set. The real secret remains readable from the TextArea.
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

	if view.Mask == "" {
		t.Error("View.Mask not set while masked")
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

	// Clearing the mask restores plaintext + clears View.Mask.
	pc.SetMasked(false)
	if view.Mask != "" {
		t.Error("View.Mask not cleared after SetMasked(false)")
	}
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
