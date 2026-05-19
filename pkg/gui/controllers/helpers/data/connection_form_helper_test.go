package data

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/afero"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/i18n"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// stringAttempt scripts one call to PromptString: a sequence of raw inputs
// the fake will try in order, each passed to the validate callback. If
// validate returns nil for an input that input is the result; if all inputs
// fail validation the test panics so the contract violation is visible.
// If `cancel` is true the prompter returns PromptCanceledErr() instead of
// running through inputs.
type stringAttempt struct {
	inputs []string
	cancel bool

	// lastErr captures the most recent validation error for assertions.
	lastErr error
}

type choiceAttempt struct {
	pick   string
	cancel bool
}

// fakePrompter is a scripted ChainedPrompter for tests. PromptString /
// PromptChoice each consume one entry from the corresponding queue per call.
type fakePrompter struct {
	t       *testing.T
	strings []*stringAttempt
	choices []*choiceAttempt

	// promptChoiceCalls counts PromptChoice invocations so tests can assert
	// it was never called (e.g. when the helper short-circuits before
	// reaching the prompt). Incremented before any cancel/dispatch logic.
	promptChoiceCalls int
}

func (f *fakePrompter) PromptString(_ context.Context, _ string, _ string, validate func(string) error) (string, error) {
	if len(f.strings) == 0 {
		f.t.Fatalf("PromptString: no scripted attempts left")
	}
	a := f.strings[0]
	f.strings = f.strings[1:]
	if a.cancel {
		return "", PromptCanceledErr()
	}
	var lastErr error
	for _, in := range a.inputs {
		err := validate(in)
		if err == nil {
			a.lastErr = nil
			return in, nil
		}
		lastErr = err
	}
	a.lastErr = lastErr
	f.t.Fatalf("PromptString: all %d scripted inputs failed validation; last err = %v", len(a.inputs), lastErr)
	return "", nil
}

func (f *fakePrompter) PromptChoice(_ context.Context, _ string, _ string, choices []string) (string, error) {
	f.promptChoiceCalls++
	if len(f.choices) == 0 {
		f.t.Fatalf("PromptChoice: no scripted attempts left")
	}
	a := f.choices[0]
	f.choices = f.choices[1:]
	if a.cancel {
		return "", PromptCanceledErr()
	}
	_ = choices // signature accepts but fake just returns the scripted pick
	return a.pick, nil
}

func newHelperForTest(t *testing.T, fs afero.Fs, drivers []string) *ConnectionFormHelper {
	t.Helper()
	tr := i18n.EnglishTranslationSet()
	c := &common.Common{Tr: tr}
	return NewConnectionFormHelper(c, fs, "/connections.yml", func() []string { return drivers })
}

func TestWalkAddConnection_HappyPath(t *testing.T) {
	fs := afero.NewMemMapFs()
	h := newHelperForTest(t, fs, []string{"postgres", "mysql"})
	prompter := &fakePrompter{
		t:       t,
		choices: []*choiceAttempt{{pick: "postgres"}},
		strings: []*stringAttempt{
			{inputs: []string{"local"}},
			{inputs: []string{"postgres://localhost/dev"}},
		},
	}
	var got models.Connection
	called := atomic.Bool{}
	err := h.WalkAddConnection(context.Background(), prompter, func(c models.Connection) {
		got = c
		called.Store(true)
	})
	if err != nil {
		t.Fatalf("WalkAddConnection: %v", err)
	}
	if !called.Load() {
		t.Fatal("onComplete was not called")
	}
	if got.Name != "local" || got.Driver != "postgres" || got.DSN != "postgres://localhost/dev" {
		t.Errorf("conn = %+v; want {local, postgres, postgres://localhost/dev}", got)
	}
	loaded, err := config.LoadConnections(fs, "/connections.yml")
	if err != nil {
		t.Fatalf("LoadConnections: %v", err)
	}
	if len(loaded) != 1 || loaded[0].Name != "local" {
		t.Errorf("file contents = %+v; want one profile 'local'", loaded)
	}
}

func TestWalkAddConnection_EscAtEachStep(t *testing.T) {
	cases := []struct {
		name string
		make func() *fakePrompter
	}{
		{
			name: "esc_step1_driver",
			make: func() *fakePrompter {
				return &fakePrompter{
					choices: []*choiceAttempt{{cancel: true}},
				}
			},
		},
		{
			name: "esc_step2_name",
			make: func() *fakePrompter {
				return &fakePrompter{
					choices: []*choiceAttempt{{pick: "postgres"}},
					strings: []*stringAttempt{{cancel: true}},
				}
			},
		},
		{
			name: "esc_step3_dsn",
			make: func() *fakePrompter {
				return &fakePrompter{
					choices: []*choiceAttempt{{pick: "postgres"}},
					strings: []*stringAttempt{
						{inputs: []string{"local"}},
						{cancel: true},
					},
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			h := newHelperForTest(t, fs, []string{"postgres"})
			p := c.make()
			p.t = t
			called := atomic.Bool{}
			err := h.WalkAddConnection(context.Background(), p, func(models.Connection) {
				called.Store(true)
			})
			if err != nil {
				t.Fatalf("WalkAddConnection: %v (expected nil)", err)
			}
			if called.Load() {
				t.Error("onComplete called after Esc; expected no-write")
			}
			if exists, _ := afero.Exists(fs, "/connections.yml"); exists {
				t.Error("connections.yml created after Esc; expected no write")
			}
		})
	}
}

func TestWalkAddConnection_UnregisteredDriverReprompts(t *testing.T) {
	fs := afero.NewMemMapFs()
	h := newHelperForTest(t, fs, []string{"postgres"})
	prompter := &fakePrompter{
		t: t,
		choices: []*choiceAttempt{
			{pick: "sqlite"},   // not registered → loop
			{pick: "postgres"}, // valid → break
		},
		strings: []*stringAttempt{
			{inputs: []string{"local"}},
			{inputs: []string{"postgres://localhost/dev"}},
		},
	}
	err := h.WalkAddConnection(context.Background(), prompter, nil)
	if err != nil {
		t.Fatalf("WalkAddConnection: %v", err)
	}
	if len(prompter.choices) != 0 {
		t.Errorf("choices not fully consumed: %d remaining", len(prompter.choices))
	}
}

func TestWalkAddConnection_NoDrivers(t *testing.T) {
	cases := []struct {
		name      string
		driversFn func() []string
	}{
		{name: "nil_slice", driversFn: func() []string { return nil }},
		{name: "zero_length", driversFn: func() []string { return []string{} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			tr := i18n.EnglishTranslationSet()
			cc := &common.Common{Tr: tr}
			h := NewConnectionFormHelper(cc, fs, "/connections.yml", c.driversFn)
			fp := &fakePrompter{t: t}
			called := atomic.Bool{}
			err := h.WalkAddConnection(context.Background(), fp, func(models.Connection) {
				called.Store(true)
			})
			if err == nil {
				t.Fatal("WalkAddConnection: nil error; want non-nil")
			}
			if !strings.Contains(err.Error(), "no drivers") {
				t.Errorf("err = %q; want it to contain %q", err.Error(), "no drivers")
			}
			if fp.promptChoiceCalls != 0 {
				t.Errorf("PromptChoice was called %d time(s); want 0", fp.promptChoiceCalls)
			}
			if called.Load() {
				t.Error("onComplete called after short-circuit; expected no-write")
			}
			if exists, _ := afero.Exists(fs, "/connections.yml"); exists {
				t.Error("connections.yml written; expected no write on empty drivers")
			}
		})
	}
}

func TestWalkAddConnection_DuplicateNameReprompts(t *testing.T) {
	fs := afero.NewMemMapFs()
	if err := config.AppendConnection(fs, "/connections.yml", models.Connection{Name: "local", Driver: "postgres", DSN: "postgres://x"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h := newHelperForTest(t, fs, []string{"postgres"})
	nameAttempt := &stringAttempt{inputs: []string{"local", "other"}}
	prompter := &fakePrompter{
		t:       t,
		choices: []*choiceAttempt{{pick: "postgres"}},
		strings: []*stringAttempt{
			nameAttempt,
			{inputs: []string{"postgres://localhost/dev"}},
		},
	}
	err := h.WalkAddConnection(context.Background(), prompter, nil)
	if err != nil {
		t.Fatalf("WalkAddConnection: %v", err)
	}
	// The duplicate-name input must have produced a validation error mentioning the Tr key text.
	// Surface lastErr via the attempt.
	if nameAttempt.lastErr != nil {
		t.Errorf("expected final attempt success; lastErr=%v", nameAttempt.lastErr)
	}
	loaded, _ := config.LoadConnections(fs, "/connections.yml")
	if len(loaded) != 2 {
		t.Fatalf("expected 2 profiles after walk, got %d", len(loaded))
	}
}

func TestWalkAddConnection_DuplicateNameError_HasTrText(t *testing.T) {
	fs := afero.NewMemMapFs()
	if err := config.AppendConnection(fs, "/connections.yml", models.Connection{Name: "local", Driver: "postgres", DSN: "postgres://x"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h := newHelperForTest(t, fs, []string{"postgres"})
	tr := i18n.EnglishTranslationSet()

	// Use an inline prompter to capture the validation error directly.
	var nameValidateErr error
	prompter := &capturePrompter{
		choicesQueue: []string{"postgres"},
		stringHandler: func(title string, validate func(string) error) (string, error) {
			if title == "Name" {
				nameValidateErr = validate("local")
				return "other", nil
			}
			if title == "DSN" {
				return "postgres://localhost/dev", nil
			}
			return "", errors.New("unexpected title " + title)
		},
	}
	if err := h.WalkAddConnection(context.Background(), prompter, nil); err != nil {
		t.Fatalf("WalkAddConnection: %v", err)
	}
	if nameValidateErr == nil {
		t.Fatal("validate(local) returned nil; want duplicate error")
	}
	if !strings.Contains(nameValidateErr.Error(), tr.DuplicateConnectionName) {
		t.Errorf("err = %q; want it to contain %q", nameValidateErr.Error(), tr.DuplicateConnectionName)
	}
}

func TestWalkAddConnection_DSNInlinePasswordRejected(t *testing.T) {
	fs := afero.NewMemMapFs()
	h := newHelperForTest(t, fs, []string{"postgres"})
	tr := i18n.EnglishTranslationSet()
	var dsnErr error
	cp := &capturePrompter{
		choicesQueue: []string{"postgres"},
		stringHandler: func(title string, validate func(string) error) (string, error) {
			switch title {
			case "Name":
				return "local", nil
			case "DSN":
				dsnErr = validate("postgres://user:hunter2@localhost/dev")
				return "postgres://user@localhost/dev", nil
			}
			return "", errors.New("unexpected title " + title)
		},
	}
	if err := h.WalkAddConnection(context.Background(), cp, nil); err != nil {
		t.Fatalf("WalkAddConnection: %v", err)
	}
	if dsnErr == nil || !strings.Contains(dsnErr.Error(), tr.DSNInlinePassword) {
		t.Errorf("dsn validate err = %v; want it to contain %q", dsnErr, tr.DSNInlinePassword)
	}
}

func TestWalkAddConnection_EmptyDSNRejected(t *testing.T) {
	fs := afero.NewMemMapFs()
	h := newHelperForTest(t, fs, []string{"postgres"})
	tr := i18n.EnglishTranslationSet()
	var emptyErr error
	cp := &capturePrompter{
		choicesQueue: []string{"postgres"},
		stringHandler: func(title string, validate func(string) error) (string, error) {
			switch title {
			case "Name":
				return "local", nil
			case "DSN":
				emptyErr = validate("   ")
				return "postgres://localhost/dev", nil
			}
			return "", errors.New("unexpected title " + title)
		},
	}
	if err := h.WalkAddConnection(context.Background(), cp, nil); err != nil {
		t.Fatalf("WalkAddConnection: %v", err)
	}
	if emptyErr == nil || !strings.Contains(emptyErr.Error(), tr.InvalidDSN) {
		t.Errorf("dsn validate err = %v; want %q", emptyErr, tr.InvalidDSN)
	}
}

// capturePrompter is a second fake prompter that gives tests direct access
// to the validate callback so they can assert on the error text WITHOUT
// triggering the t.Fatalf path inside the looped fake.
type capturePrompter struct {
	choicesQueue  []string
	stringHandler func(title string, validate func(string) error) (string, error)
}

func (c *capturePrompter) PromptString(_ context.Context, title string, _ string, validate func(string) error) (string, error) {
	return c.stringHandler(title, validate)
}

func (c *capturePrompter) PromptChoice(_ context.Context, _ string, _ string, _ []string) (string, error) {
	if len(c.choicesQueue) == 0 {
		return "", errors.New("capturePrompter: no choices left")
	}
	pick := c.choicesQueue[0]
	c.choicesQueue = c.choicesQueue[1:]
	return pick, nil
}

func TestNewEmptyStateHook(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	t.Run("empty list renders hint", func(t *testing.T) {
		hook := NewEmptyStateHook(tr, func() []models.Connection { return nil })
		render, hint := hook(nil)
		if !render {
			t.Errorf("renderEmpty = false; want true")
		}
		if hint != tr.EmptyConnectionsHint {
			t.Errorf("hint = %q; want %q", hint, tr.EmptyConnectionsHint)
		}
		// Pin the exact copy so it can't accidentally regrow and re-truncate
		// in the connections rail (dbsavvy-tro.8).
		const want = "No connections yet.\nPress a to add"
		if hint != want {
			t.Errorf("hint = %q; want %q", hint, want)
		}
	})
	t.Run("non-empty list", func(t *testing.T) {
		hook := NewEmptyStateHook(tr, func() []models.Connection {
			return []models.Connection{{Name: "x"}}
		})
		render, hint := hook(nil)
		if render {
			t.Errorf("renderEmpty = true; want false")
		}
		if hint != "" {
			t.Errorf("hint = %q; want empty", hint)
		}
	})
	t.Run("nil provider is safe", func(t *testing.T) {
		hook := NewEmptyStateHook(tr, nil)
		render, _ := hook(nil)
		if render {
			t.Errorf("renderEmpty = true; want false on nil provider")
		}
	})
}

func TestShouldShowFirstRunTip(t *testing.T) {
	t.Run("empty + unseen → true", func(t *testing.T) {
		store := common.NewAppStateStore(afero.NewMemMapFs(), "/s.yml", nil)
		got := ShouldShowFirstRunTip(store, func() []models.Connection { return nil })
		if !got {
			t.Errorf("got false; want true")
		}
	})
	t.Run("seen → false", func(t *testing.T) {
		store := common.NewAppStateStore(afero.NewMemMapFs(), "/s.yml", nil)
		store.StampStartupTips()
		_ = store.Flush()
		got := ShouldShowFirstRunTip(store, func() []models.Connection { return nil })
		if got {
			t.Errorf("got true; want false (already seen)")
		}
	})
	t.Run("non-empty list → false", func(t *testing.T) {
		store := common.NewAppStateStore(afero.NewMemMapFs(), "/s.yml", nil)
		got := ShouldShowFirstRunTip(store, func() []models.Connection {
			return []models.Connection{{Name: "x"}}
		})
		if got {
			t.Errorf("got true; want false (list non-empty)")
		}
	})
	t.Run("nil inputs safe", func(t *testing.T) {
		if ShouldShowFirstRunTip(nil, nil) {
			t.Errorf("nil inputs → true; want false")
		}
	})
}
