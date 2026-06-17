package config

import (
	"errors"
	"os"
	"testing"

	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/models"
)

func names(qs []models.SavedQuery) []string {
	out := make([]string, len(qs))
	for i := range qs {
		out[i] = qs[i].Name
	}
	return out
}

func TestQueriesLoadMissingFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	got, err := LoadQueries(fs, "/no/such/queries.yml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatalf("got nil slice, want empty non-nil slice")
	}
	if len(got) != 0 {
		t.Errorf("got %d entries, want 0", len(got))
	}
}

func TestQueriesSaveModeAndRoundTrip(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/cfg/queries.yml"
	in := []models.SavedQuery{
		{Name: "a", SQL: "select 1"},
		{Name: "b", SQL: "select 2"},
	}
	if err := SaveQueries(fs, path, in); err != nil {
		t.Fatalf("SaveQueries: %v", err)
	}

	info, err := fs.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != os.FileMode(0o600) {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}

	got, err := LoadQueries(fs, path)
	if err != nil {
		t.Fatalf("LoadQueries: %v", err)
	}
	if len(got) != len(in) {
		t.Fatalf("round-trip count = %d, want %d", len(got), len(in))
	}
	for i := range in {
		if got[i] != in[i] {
			t.Errorf("round-trip[%d] = %+v, want %+v", i, got[i], in[i])
		}
	}
}

func TestQueriesSaveEmptySlice(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/cfg/queries.yml"
	if err := SaveQueries(fs, path, []models.SavedQuery{}); err != nil {
		t.Fatalf("SaveQueries empty: %v", err)
	}
	got, err := LoadQueries(fs, path)
	if err != nil {
		t.Fatalf("LoadQueries: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d entries, want 0", len(got))
	}
}

func TestQueriesUpsertReplaceInPlace(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/cfg/queries.yml"
	start := []models.SavedQuery{
		{Name: "a", SQL: "sa"},
		{Name: "b", SQL: "sb"},
		{Name: "c", SQL: "sc"},
	}
	if err := SaveQueries(fs, path, start); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := UpsertQuery(fs, path, models.SavedQuery{Name: "b", SQL: "sb-new"}); err != nil {
		t.Fatalf("UpsertQuery: %v", err)
	}

	got, err := LoadQueries(fs, path)
	if err != nil {
		t.Fatalf("LoadQueries: %v", err)
	}
	wantNames := []string{"a", "b", "c"}
	gotNames := names(got)
	if len(gotNames) != len(wantNames) {
		t.Fatalf("names = %v, want %v", gotNames, wantNames)
	}
	for i := range wantNames {
		if gotNames[i] != wantNames[i] {
			t.Fatalf("order changed: names = %v, want %v", gotNames, wantNames)
		}
	}
	if got[1].SQL != "sb-new" {
		t.Errorf("b.SQL = %q, want %q", got[1].SQL, "sb-new")
	}
}

func TestQueriesUpsertAppendsWhenAbsent(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/cfg/queries.yml"
	if err := SaveQueries(fs, path, []models.SavedQuery{{Name: "a", SQL: "sa"}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := UpsertQuery(fs, path, models.SavedQuery{Name: "z", SQL: "sz"}); err != nil {
		t.Fatalf("UpsertQuery: %v", err)
	}
	got, err := LoadQueries(fs, path)
	if err != nil {
		t.Fatalf("LoadQueries: %v", err)
	}
	if len(got) != 2 || got[1].Name != "z" {
		t.Errorf("got %v, want [a z]", names(got))
	}
}

func TestQueriesAppendDuplicateErrors(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/cfg/queries.yml"
	if err := AppendQuery(fs, path, models.SavedQuery{Name: "a", SQL: "sa"}); err != nil {
		t.Fatalf("first append: %v", err)
	}
	err := AppendQuery(fs, path, models.SavedQuery{Name: "a", SQL: "other"})
	if !errors.Is(err, ErrDuplicateQueryName) {
		t.Fatalf("append duplicate err = %v, want ErrDuplicateQueryName", err)
	}
	got, _ := LoadQueries(fs, path)
	if len(got) != 1 {
		t.Errorf("got %d entries after rejected append, want 1", len(got))
	}
}

func TestQueriesTrimmedNameCollision(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/cfg/queries.yml"
	if err := AppendQuery(fs, path, models.SavedQuery{Name: "foo", SQL: "sa"}); err != nil {
		t.Fatalf("first append: %v", err)
	}

	// Append of "foo " must collide with "foo".
	if err := AppendQuery(fs, path, models.SavedQuery{Name: "foo ", SQL: "sb"}); !errors.Is(err, ErrDuplicateQueryName) {
		t.Fatalf("append \"foo \" err = %v, want ErrDuplicateQueryName", err)
	}

	// Upsert of "foo " must replace "foo" in place (no duplicate entry).
	if err := UpsertQuery(fs, path, models.SavedQuery{Name: "foo ", SQL: "sb"}); err != nil {
		t.Fatalf("UpsertQuery \"foo \": %v", err)
	}
	got, err := LoadQueries(fs, path)
	if err != nil {
		t.Fatalf("LoadQueries: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1 (no duplicate)", len(got))
	}
	if got[0].SQL != "sb" {
		t.Errorf("SQL = %q, want %q", got[0].SQL, "sb")
	}
}

func TestQueriesDelete(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/cfg/queries.yml"
	seed := []models.SavedQuery{
		{Name: "a", SQL: "sa"},
		{Name: "b", SQL: "sb"},
	}
	if err := SaveQueries(fs, path, seed); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := DeleteQuery(fs, path, "a"); err != nil {
		t.Fatalf("DeleteQuery: %v", err)
	}
	got, _ := LoadQueries(fs, path)
	if len(got) != 1 || got[0].Name != "b" {
		t.Errorf("after delete got %v, want [b]", names(got))
	}

	// Deleting an absent name is a no-op, no error.
	if err := DeleteQuery(fs, path, "ghost"); err != nil {
		t.Fatalf("DeleteQuery absent = %v, want nil", err)
	}
	got2, _ := LoadQueries(fs, path)
	if len(got2) != 1 || got2[0].Name != "b" {
		t.Errorf("absent delete mutated file: got %v, want [b]", names(got2))
	}
}

// A ReadOnlyFs over a populated base makes AtomicWriteYAML's write fail.
func TestQueriesSaveWriteFailureLeavesFileIntact(t *testing.T) {
	base := afero.NewMemMapFs()
	path := "/cfg/queries.yml"
	original := []models.SavedQuery{{Name: "a", SQL: "sa"}}
	if err := SaveQueries(base, path, original); err != nil {
		t.Fatalf("seed: %v", err)
	}

	ro := afero.NewReadOnlyFs(base)
	err := SaveQueries(ro, path, []models.SavedQuery{{Name: "b", SQL: "sb"}})
	if err == nil {
		t.Fatal("expected write error on ReadOnlyFs, got nil")
	}

	// Prior contents intact (read through the base fs).
	got, lerr := LoadQueries(base, path)
	if lerr != nil {
		t.Fatalf("LoadQueries after failed write: %v", lerr)
	}
	if len(got) != 1 || got[0].Name != "a" || got[0].SQL != "sa" {
		t.Errorf("file changed after failed write: got %v", got)
	}
}
