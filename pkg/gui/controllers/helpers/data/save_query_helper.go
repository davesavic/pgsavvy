package data

import (
	"context"
	"errors"
	"strings"

	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// errEmptyQueryName is the sentinel WalkSaveQuery returns when the user
// submits a whitespace-only name. The caller (QueryEditorController) maps it
// to a single "name must not be empty" toast and writes nothing. Distinct
// from the canceled sentinel so the caller can phrase the two cases
// differently if it chooses.
var errEmptyQueryName = errors.New("save_query: name must not be empty")

// SaveQueryHelper walks the user through the name prompt + overwrite-confirm
// sequence and (on success) persists the captured SQL to queries.yml via
// config.AppendQuery / config.UpsertQuery.
//
// It mirrors ConnectionFormHelper: stateless aside from the i18n strings and
// the filesystem + path the config writers touch. The Prompt→Confirm flow is
// driven through the blocking ChainedPrompter (PromptString then, on a name
// collision, PromptChoice) rather than nesting ConfirmHelper.Confirm inside
// PromptHelper.onSubmit — that nesting double-pops the focus stack (both
// helpers issue an unconditional tree.Pop()).
type SaveQueryHelper struct {
	common *common.Common
	fs     afero.Fs
	path   string
}

// NewSaveQueryHelper constructs a SaveQueryHelper. The supplied *common.Common
// carries the i18n TranslationSet; fs + path locate the queries.yml file the
// AppendQuery / UpsertQuery calls write to.
func NewSaveQueryHelper(c *common.Common, fs afero.Fs, path string) *SaveQueryHelper {
	return &SaveQueryHelper{common: c, fs: fs, path: path}
}

// WalkSaveQuery runs the save sequence for the already-captured (and trimmed)
// sql:
//
//  1. name  — PromptString. The trimmed name is the storage key; a
//     whitespace-only name returns errEmptyQueryName (no write).
//  2. collision — LoadQueries; if the trimmed name already exists,
//     PromptChoice("Overwrite \"name\"?", ["Overwrite","Cancel"]). Cancel
//     aborts with no write; Overwrite replaces in place via UpsertQuery.
//     No collision -> AppendQuery.
//
// The captured sql is stored VERBATIM as ONE SavedQuery (a multi-statement
// visual selection is one entry, never split into N).
//
// Esc at either prompt (M10i) discards and returns nil via translateCancel.
// On a successful write the trimmed name is returned so the caller can toast
// it. Other errors (filesystem, LoadQueries) are returned verbatim.
func (h *SaveQueryHelper) WalkSaveQuery(ctx context.Context, prompter ChainedPrompter, sql string) (string, error) {
	if prompter == nil {
		return "", errors.New("save_query: nil prompter")
	}
	tr := h.tr()

	name, err := h.promptName(ctx, prompter, tr)
	if err != nil {
		return "", h.translateSaveCancel(err)
	}
	if name == "" {
		return "", errEmptyQueryName
	}

	q := models.SavedQuery{Name: name, SQL: sql}

	existing, err := config.LoadQueries(h.fs, h.path)
	if err != nil {
		return "", err
	}
	if !queryNameExists(existing, name) {
		if err := config.AppendQuery(h.fs, h.path, q); err != nil {
			return "", err
		}
		return name, nil
	}

	overwrite, err := prompter.PromptChoice(ctx, "Save query", "Overwrite \""+name+"\"?", []string{"Overwrite", "Cancel"})
	if err != nil {
		return "", h.translateSaveCancel(err)
	}
	if overwrite != "Overwrite" {
		return "", nil
	}
	if err := config.UpsertQuery(h.fs, h.path, q); err != nil {
		return "", err
	}
	return name, nil
}

// promptName runs the name prompt. The validate callback only rejects an
// empty name (a duplicate is NOT a validation error here — it routes to the
// overwrite-confirm choice instead). The returned name is trimmed ONCE so the
// storage comparison (also trimmed) and the PromptChoice dialog use the same
// string.
func (h *SaveQueryHelper) promptName(ctx context.Context, prompter ChainedPrompter, _ *i18n.TranslationSet) (string, error) {
	got, err := prompter.PromptString(ctx, "Save query", "Query name", nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(got), nil
}

// translateSaveCancel converts errPromptCanceled into a nil error (clean
// exit); every other error is returned as-is.
func (h *SaveQueryHelper) translateSaveCancel(err error) error {
	if errors.Is(err, errPromptCanceled) {
		return nil
	}
	return err
}

// tr returns the active TranslationSet, falling back to a fresh English set
// if no Common was supplied (test-friendliness).
func (h *SaveQueryHelper) tr() *i18n.TranslationSet {
	if h.common != nil && h.common.Tr != nil {
		return h.common.Tr
	}
	return i18n.EnglishTranslationSet()
}

// SaveQueryEmptyNameErr exposes the empty-name sentinel so callers outside the
// data package (QueryEditorController) can map it to a toast.
func SaveQueryEmptyNameErr() error { return errEmptyQueryName }

// queryNameExists reports whether any entry's trimmed Name equals key (key is
// already trimmed). Matches the storage writers' uniqueness rule so "foo" and
// "foo " collide.
func queryNameExists(qs []models.SavedQuery, key string) bool {
	for i := range qs {
		if strings.TrimSpace(qs[i].Name) == key {
			return true
		}
	}
	return false
}
