package data

import (
	"context"
	"errors"
	"testing"

	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// saveFakePrompter is a scripted ChainedPrompter for the SaveQueryHelper
// tests. It is distinct from the connection-form fakePrompter because
// promptName here passes a nil validate (an empty name is NOT a re-prompt, it
// returns the empty sentinel), so the validate-driving fakePrompter would
// panic on nil. This fake records its calls so tests can assert that
// PromptChoice fires ONLY on a collision (never a nested Confirm).
type saveFakePrompter struct {
	t *testing.T

	nameInput  string
	nameCancel bool

	choicePick   string
	choiceCancel bool

	stringCalls int
	choiceCalls int
}

func (f *saveFakePrompter) PromptString(_ context.Context, _, _ string, validate func(string) error) (string, error) {
	f.stringCalls++
	if f.nameCancel {
		return "", PromptCanceledErr()
	}
	if validate != nil {
		if err := validate(f.nameInput); err != nil {
			f.t.Fatalf("PromptString: unexpected validate error %v", err)
		}
	}
	return f.nameInput, nil
}

func (f *saveFakePrompter) PromptChoice(_ context.Context, _, _ string, choices []string) (string, error) {
	f.choiceCalls++
	if f.choiceCancel {
		return "", PromptCanceledErr()
	}
	for _, c := range choices {
		if c == f.choicePick {
			return f.choicePick, nil
		}
	}
	f.t.Fatalf("PromptChoice: scripted pick %q not in choices %v", f.choicePick, choices)
	return "", nil
}

func newSaveHelperForTest(fs afero.Fs) *SaveQueryHelper {
	c := &common.Common{Tr: i18n.EnglishTranslationSet()}
	return NewSaveQueryHelper(c, fs, "/queries.yml")
}

func TestWalkSaveQuery_FreshNameAppends(t *testing.T) {
	fs := afero.NewMemMapFs()
	h := newSaveHelperForTest(fs)
	p := &saveFakePrompter{t: t, nameInput: "my report"}

	name, err := h.WalkSaveQuery(context.Background(), p, "SELECT 1;")
	if err != nil {
		t.Fatalf("WalkSaveQuery: %v", err)
	}
	if name != "my report" {
		t.Errorf("name = %q, want %q", name, "my report")
	}
	if p.choiceCalls != 0 {
		t.Errorf("PromptChoice called %d times on a fresh name; want 0", p.choiceCalls)
	}
	got, _ := config.LoadQueries(fs, "/queries.yml")
	if len(got) != 1 || got[0].Name != "my report" || got[0].SQL != "SELECT 1;" {
		t.Errorf("stored = %+v; want one {my report, SELECT 1;}", got)
	}
}

func TestWalkSaveQuery_NameTrimmedOnce(t *testing.T) {
	fs := afero.NewMemMapFs()
	h := newSaveHelperForTest(fs)
	// "  foo  " trims to "foo": stored under the trimmed key.
	p := &saveFakePrompter{t: t, nameInput: "  foo  "}

	name, err := h.WalkSaveQuery(context.Background(), p, "SELECT 1;")
	if err != nil {
		t.Fatalf("WalkSaveQuery: %v", err)
	}
	if name != "foo" {
		t.Errorf("name = %q, want %q", name, "foo")
	}
	got, _ := config.LoadQueries(fs, "/queries.yml")
	if len(got) != 1 || got[0].Name != "foo" {
		t.Errorf("stored name = %+v; want trimmed 'foo'", got)
	}
}

func TestWalkSaveQuery_WhitespaceNameNoWrite(t *testing.T) {
	fs := afero.NewMemMapFs()
	h := newSaveHelperForTest(fs)
	p := &saveFakePrompter{t: t, nameInput: "   "}

	name, err := h.WalkSaveQuery(context.Background(), p, "SELECT 1;")
	if !errors.Is(err, SaveQueryEmptyNameErr()) {
		t.Fatalf("err = %v, want SaveQueryEmptyNameErr", err)
	}
	if name != "" {
		t.Errorf("name = %q, want empty", name)
	}
	if p.choiceCalls != 0 {
		t.Errorf("PromptChoice called %d times on whitespace name; want 0", p.choiceCalls)
	}
	got, _ := config.LoadQueries(fs, "/queries.yml")
	if len(got) != 0 {
		t.Errorf("stored = %+v; want no write", got)
	}
}

func TestWalkSaveQuery_NameCancelNoWrite(t *testing.T) {
	fs := afero.NewMemMapFs()
	h := newSaveHelperForTest(fs)
	p := &saveFakePrompter{t: t, nameCancel: true}

	name, err := h.WalkSaveQuery(context.Background(), p, "SELECT 1;")
	if err != nil {
		t.Fatalf("cancel should map to nil err; got %v", err)
	}
	if name != "" {
		t.Errorf("name = %q, want empty", name)
	}
	got, _ := config.LoadQueries(fs, "/queries.yml")
	if len(got) != 0 {
		t.Errorf("stored = %+v; want no write on cancel", got)
	}
}

func TestWalkSaveQuery_CollisionOverwriteUpserts(t *testing.T) {
	fs := afero.NewMemMapFs()
	// Seed an existing entry "foo".
	if err := config.AppendQuery(fs, "/queries.yml", models.SavedQuery{Name: "foo", SQL: "OLD"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h := newSaveHelperForTest(fs)
	p := &saveFakePrompter{t: t, nameInput: "foo", choicePick: "Overwrite"}

	name, err := h.WalkSaveQuery(context.Background(), p, "NEW")
	if err != nil {
		t.Fatalf("WalkSaveQuery: %v", err)
	}
	if name != "foo" {
		t.Errorf("name = %q, want foo", name)
	}
	if p.choiceCalls != 1 {
		t.Errorf("PromptChoice called %d times; want exactly 1 (collision path)", p.choiceCalls)
	}
	got, _ := config.LoadQueries(fs, "/queries.yml")
	// Upsert replaces in place — still ONE entry, SQL now "NEW".
	if len(got) != 1 || got[0].SQL != "NEW" {
		t.Errorf("stored = %+v; want one {foo, NEW} (replaced in place)", got)
	}
}

func TestWalkSaveQuery_CollisionCancelNoWrite(t *testing.T) {
	fs := afero.NewMemMapFs()
	if err := config.AppendQuery(fs, "/queries.yml", models.SavedQuery{Name: "foo", SQL: "OLD"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h := newSaveHelperForTest(fs)
	p := &saveFakePrompter{t: t, nameInput: "foo", choicePick: "Cancel"}

	name, err := h.WalkSaveQuery(context.Background(), p, "NEW")
	if err != nil {
		t.Fatalf("WalkSaveQuery: %v", err)
	}
	if name != "" {
		t.Errorf("name = %q, want empty (cancel)", name)
	}
	if p.choiceCalls != 1 {
		t.Errorf("PromptChoice called %d times; want exactly 1", p.choiceCalls)
	}
	got, _ := config.LoadQueries(fs, "/queries.yml")
	if len(got) != 1 || got[0].SQL != "OLD" {
		t.Errorf("stored = %+v; want unchanged {foo, OLD} on cancel", got)
	}
}

// TestWalkSaveQuery_CollisionDetectedAcrossTrim asserts "foo" collides with an
// existing "foo " entry (the storage uniqueness key is the trimmed name).
func TestWalkSaveQuery_CollisionDetectedAcrossTrim(t *testing.T) {
	fs := afero.NewMemMapFs()
	// Seed an entry whose on-disk Name has a trailing space.
	if err := config.SaveQueries(fs, "/queries.yml", []models.SavedQuery{{Name: "foo ", SQL: "OLD"}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h := newSaveHelperForTest(fs)
	p := &saveFakePrompter{t: t, nameInput: "foo", choicePick: "Overwrite"}

	name, err := h.WalkSaveQuery(context.Background(), p, "NEW")
	if err != nil {
		t.Fatalf("WalkSaveQuery: %v", err)
	}
	if name != "foo" {
		t.Errorf("name = %q, want foo", name)
	}
	if p.choiceCalls != 1 {
		t.Errorf("PromptChoice called %d times; want 1 ('foo' vs 'foo ' collision)", p.choiceCalls)
	}
	got, _ := config.LoadQueries(fs, "/queries.yml")
	if len(got) != 1 || got[0].SQL != "NEW" {
		t.Errorf("stored = %+v; want one entry replaced in place", got)
	}
}

// TestWalkSaveQuery_MultiStatementStoredVerbatim asserts a multi-statement
// blob is persisted as ONE entry (NOT split into N).
func TestWalkSaveQuery_MultiStatementStoredVerbatim(t *testing.T) {
	fs := afero.NewMemMapFs()
	h := newSaveHelperForTest(fs)
	p := &saveFakePrompter{t: t, nameInput: "batch"}
	sql := "SELECT 1; SELECT 2; SELECT 3;"

	if _, err := h.WalkSaveQuery(context.Background(), p, sql); err != nil {
		t.Fatalf("WalkSaveQuery: %v", err)
	}
	got, _ := config.LoadQueries(fs, "/queries.yml")
	if len(got) != 1 {
		t.Fatalf("stored %d entries; want exactly 1 (verbatim, not split)", len(got))
	}
	if got[0].SQL != sql {
		t.Errorf("stored SQL = %q; want verbatim %q", got[0].SQL, sql)
	}
}
