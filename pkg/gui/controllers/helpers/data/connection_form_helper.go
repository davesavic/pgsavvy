package data

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"

	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// errPromptCanceled is the sentinel returned by ChainedPrompter implementations
// when the user presses Esc. WalkAddConnection translates it into a clean
// no-write exit; the caller of WalkAddConnection sees a nil error.
var errPromptCanceled = errors.New("connection_form: prompt canceled")

// PromptCanceledErr exposes the canceled-prompt sentinel for ChainedPrompter
// implementations (T7b owns the real prompt UI) to use as their cancel value.
// The Walk routines below treat it as a clean exit.
func PromptCanceledErr() error { return errPromptCanceled }

// ChainedPrompter is the minimal prompt surface WalkAddConnection requires.
// T7b implements this against the gocui prompt widget; tests inject a fake.
//
// Each method returns PromptCanceledErr() if the user pressed Esc. Validation
// failures inside the WalkAddConnection step are NOT surfaced through this
// interface — the helper re-prompts internally by calling the same method
// again with an updated label.
type ChainedPrompter interface {
	// PromptString asks for a free-form string. validate runs on the raw
	// input AFTER trim; if it returns a non-nil error the prompter is
	// expected to surface the error to the user and re-prompt within the
	// same call until validate returns nil or the user cancels with Esc.
	PromptString(ctx context.Context, title, label string, validate func(string) error) (string, error)

	// PromptChoice asks the user to pick one of choices. Returning a value
	// that is NOT in choices is a contract violation; WalkAddConnection
	// defensively re-prompts on such input.
	PromptChoice(ctx context.Context, title, label string, choices []string) (string, error)
}

// ConnectionFormHelper walks the user through the three-step
// driver→name→DSN prompt sequence and (on success) writes the new profile to
// connections.yml via config.AppendConnection.
//
// The helper is stateless aside from the dependencies it carries: the i18n
// strings, the filesystem + path used by config.AppendConnection /
// LoadConnections, and the driver-registry accessor (defaulting to
// drivers.Names; overridable for tests).
type ConnectionFormHelper struct {
	common *common.Common
	fs     afero.Fs
	path   string

	// driversFn returns the registered driver names. Defaults to
	// drivers.Names. Tests override this seam.
	driversFn func() []string
}

// NewConnectionFormHelper constructs a ConnectionFormHelper. The supplied
// *common.Common carries the i18n TranslationSet; fs + path locate the
// connections.yml file the AppendConnection call writes to. A nil driversFn
// defaults to pkg/drivers.Names.
func NewConnectionFormHelper(c *common.Common, fs afero.Fs, path string, driversFn func() []string) *ConnectionFormHelper {
	if driversFn == nil {
		driversFn = drivers.Names
	}
	return &ConnectionFormHelper{
		common:    c,
		fs:        fs,
		path:      path,
		driversFn: driversFn,
	}
}

// WalkAddConnection runs the three-step prompt sequence:
//
//  1. driver  — choose from drivers.Names(); re-prompt if the choice is not
//     in the registered set.
//  2. name    — must be non-empty and must not duplicate any existing
//     profile's Name (per LoadConnections at prompt time). Tr.DuplicateConnectionName
//     is surfaced to the prompter as the validation error.
//  3. dsn     — must be non-empty and must parse via url.Parse. Per G3-G(ii)
//     any DSN that parses to userinfo with a non-empty password is rejected
//     with Tr.DSNInlinePassword.
//
// On all-valid the new profile is written via config.AppendConnection and
// onComplete is invoked with the created models.Connection.
//
// Esc at ANY step (M10i) discards collected values and returns nil. Other
// errors (ctx cancellation, filesystem write failures, an
// ErrDuplicateConnectionName race between the name check and the append)
// are returned verbatim.
func (h *ConnectionFormHelper) WalkAddConnection(ctx context.Context, prompter ChainedPrompter, onComplete func(c models.Connection)) error {
	if prompter == nil {
		return errors.New("connection_form: nil prompter")
	}
	tr := h.tr()

	// Step 1: driver.
	driver, err := h.promptDriver(ctx, prompter)
	if err != nil {
		return h.translateCancel(err)
	}

	// Step 2: name (uniqueness checked against LoadConnections at prompt time).
	name, err := h.promptName(ctx, prompter, tr)
	if err != nil {
		return h.translateCancel(err)
	}

	// Step 3: DSN (parseable, no inline password).
	dsn, err := h.promptDSN(ctx, prompter, tr)
	if err != nil {
		return h.translateCancel(err)
	}

	conn := models.Connection{
		Name:   name,
		Driver: driver,
		DSN:    dsn,
	}
	if err := config.AppendConnection(h.fs, h.path, conn); err != nil {
		return err
	}
	if onComplete != nil {
		onComplete(conn)
	}
	return nil
}

// promptDriver runs the driver choice loop. If the user picks something not
// in drivers.Names() (a contract violation by a misbehaving prompter) we
// re-prompt rather than persisting bad data.
func (h *ConnectionFormHelper) promptDriver(ctx context.Context, prompter ChainedPrompter) (string, error) {
	if len(h.driversFn()) == 0 {
		return "", errors.New("connection_form: no drivers registered")
	}
	for {
		choices := h.driversFn()
		picked, err := prompter.PromptChoice(ctx, "Driver", "Pick a driver", choices)
		if err != nil {
			return "", err
		}
		if containsString(choices, picked) {
			return picked, nil
		}
		// Out-of-set: loop and re-prompt.
	}
}

// promptName runs the name prompt; the validate callback rejects empty and
// duplicate names. ChainedPrompter implementations are expected to re-prompt
// on validation error until validate returns nil or the user cancels.
func (h *ConnectionFormHelper) promptName(ctx context.Context, prompter ChainedPrompter, tr *i18n.TranslationSet) (string, error) {
	validate := func(raw string) error {
		v := strings.TrimSpace(raw)
		if v == "" {
			return errors.New("name must not be empty")
		}
		existing, err := config.LoadConnections(h.fs, h.path)
		if err != nil {
			return err
		}
		for i := range existing {
			if existing[i].Name == v {
				return errors.New(tr.DuplicateConnectionName)
			}
		}
		return nil
	}
	got, err := prompter.PromptString(ctx, "Name", "Connection name", validate)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(got), nil
}

// promptDSN runs the DSN prompt with two validation rules (G3-G(ii)):
// non-empty + url.Parse-able + no inline userinfo password.
func (h *ConnectionFormHelper) promptDSN(ctx context.Context, prompter ChainedPrompter, tr *i18n.TranslationSet) (string, error) {
	validate := func(raw string) error {
		v := strings.TrimSpace(raw)
		if v == "" {
			return errors.New(tr.InvalidDSN)
		}
		u, err := url.Parse(v)
		if err != nil {
			return errors.New(tr.InvalidDSN)
		}
		if u.User != nil {
			if _, hasPwd := u.User.Password(); hasPwd {
				return errors.New(tr.DSNInlinePassword)
			}
		}
		return nil
	}
	got, err := prompter.PromptString(ctx, "DSN", "Connection string", validate)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(got), nil
}

// translateCancel converts errPromptCanceled into a nil error (clean exit);
// every other error is returned as-is. M10i: ESC at any step → no-write,
// no-error.
func (h *ConnectionFormHelper) translateCancel(err error) error {
	if errors.Is(err, errPromptCanceled) {
		return nil
	}
	return err
}

// tr returns the active TranslationSet, falling back to a fresh English set
// if no Common was supplied (test-friendliness; production wiring always
// supplies common).
func (h *ConnectionFormHelper) tr() *i18n.TranslationSet {
	if h.common != nil && h.common.Tr != nil {
		return h.common.Tr
	}
	return i18n.EnglishTranslationSet()
}

func containsString(xs []string, v string) bool {
	return slices.Contains(xs, v)
}

// NewEmptyStateHook returns a closure matching the
// types.ContextTreeDeps.EmptyStateHook signature. It reports renderEmpty=true
// + hint=Tr.EmptyConnectionsHint when the supplied profile-list provider
// reports an empty list.
//
// The first-run TIP POPUP is gated separately by ShouldShowFirstRunTip — the
// hook intentionally does NOT consult AppStateStore.IsStartupTipsSeen,
// because the hint copy should keep rendering even after the popup has been
// dismissed (the empty list is an ongoing state, not a one-time event).
func NewEmptyStateHook(tr *i18n.TranslationSet, profilesProvider func() []models.Connection) func(*common.Common) (bool, string) {
	return func(_ *common.Common) (bool, string) {
		if profilesProvider == nil {
			return false, ""
		}
		profs := profilesProvider()
		if len(profs) == 0 {
			hint := ""
			if tr != nil {
				hint = tr.EmptyConnectionsHint
			}
			return true, hint
		}
		return false, ""
	}
}

// ShouldShowFirstRunTip reports whether the first-run tip popup should be
// pushed onto the context stack. The predicate is read-only: it never calls
// StampStartupTips. T7b composes this with the actual popup wiring; the
// popup's dismissal handler is what calls store.StampStartupTips.
//
// The popup shows iff there are no profiles AND the user has never seen the
// tip before.
func ShouldShowFirstRunTip(store *common.AppStateStore, profilesProvider func() []models.Connection) bool {
	if store == nil || profilesProvider == nil {
		return false
	}
	if store.IsStartupTipsSeen() {
		return false
	}
	return len(profilesProvider()) == 0
}
