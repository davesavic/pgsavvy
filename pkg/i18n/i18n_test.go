package i18n

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
)

// newOverlayFS returns a MemMapFs prepopulated with translations/<lang>.json
// for each entry in files, where the key is the language code and the value
// is the JSON body to write.
func newOverlayFS(t *testing.T, files map[string]string) afero.Fs {
	t.Helper()
	fsys := afero.NewMemMapFs()
	if err := fsys.MkdirAll("translations", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for lang, body := range files {
		path := filepath.Join("translations", lang+".json")
		if err := afero.WriteFile(fsys, path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return fsys
}

func readTestdata(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "i18n", name))
	if err != nil {
		t.Fatalf("read testdata %s: %v", name, err)
	}
	return string(data)
}

func TestLoadAndMerge_EmptyOverlayPreservesEnglish(t *testing.T) {
	fsys := newOverlayFS(t, map[string]string{"empty": readTestdata(t, "empty.json")})

	got, err := LoadAndMerge(fsys, "empty")
	if err != nil {
		t.Fatalf("LoadAndMerge: %v", err)
	}
	want := EnglishTranslationSet()
	if *got != *want {
		t.Errorf("empty overlay mutated English baseline:\n got=%+v\nwant=%+v", got, want)
	}
}

func TestLoadAndMerge_PartialOverlay_OmittedFieldsKeepEnglish(t *testing.T) {
	fsys := newOverlayFS(t, map[string]string{"fr": readTestdata(t, "fr.json")})

	got, err := LoadAndMerge(fsys, "fr")
	if err != nil {
		t.Fatalf("LoadAndMerge: %v", err)
	}
	if got.OpenTable != "Ouvrir" {
		t.Errorf("OpenTable: got %q, want %q", got.OpenTable, "Ouvrir")
	}
	if got.AreYouSure != "Êtes-vous sûr ?" {
		t.Errorf("AreYouSure: got %q, want %q", got.AreYouSure, "Êtes-vous sûr ?")
	}

	// Regression guard: fields NOT present in the overlay must keep their
	// English defaults (NOT zero-valued strings).
	english := EnglishTranslationSet()
	if got.TruncateTable != english.TruncateTable {
		t.Errorf("TruncateTable should keep English default; got %q want %q", got.TruncateTable, english.TruncateTable)
	}
	if got.DropTable != english.DropTable {
		t.Errorf("DropTable should keep English default; got %q want %q", got.DropTable, english.DropTable)
	}
	if got.ConnectionLost != english.ConnectionLost {
		t.Errorf("ConnectionLost should keep English default; got %q want %q", got.ConnectionLost, english.ConnectionLost)
	}
	if got.Actions.RunQuery != english.Actions.RunQuery {
		t.Errorf("Actions.RunQuery should keep English default; got %q want %q", got.Actions.RunQuery, english.Actions.RunQuery)
	}
}

func TestLoadAndMerge_MalformedJSON_FallsBackToFreshEnglish(t *testing.T) {
	fsys := newOverlayFS(t, map[string]string{"bad": readTestdata(t, "malformed.json")})

	got, err := LoadAndMerge(fsys, "bad")
	if err != nil {
		t.Fatalf("LoadAndMerge: %v", err)
	}
	want := EnglishTranslationSet()
	if *got != *want {
		t.Errorf("malformed overlay should yield fresh English baseline:\n got=%+v\nwant=%+v", got, want)
	}
}

func TestLoadAndMerge_Oversize_FallsBackToEnglish_NoPanic(t *testing.T) {
	// Generate >1 MiB JSON body on the fly to avoid checking a large
	// fixture into git. Valid JSON object but oversize.
	big := `{"OpenTable":"` + strings.Repeat("a", (1<<20)+100) + `"}`
	fsys := newOverlayFS(t, map[string]string{"big": big})

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("LoadAndMerge panicked on oversize overlay: %v", r)
		}
	}()

	got, err := LoadAndMerge(fsys, "big")
	if err != nil {
		t.Fatalf("LoadAndMerge: %v", err)
	}
	want := EnglishTranslationSet()
	if *got != *want {
		t.Errorf("oversize overlay should yield English baseline:\n got=%+v\nwant=%+v", got, want)
	}
}

func TestLoadAndMerge_NonexistentLang_ReturnsEnglish_NilError(t *testing.T) {
	fsys := afero.NewMemMapFs() // empty
	got, err := LoadAndMerge(fsys, "zz")
	if err != nil {
		t.Fatalf("LoadAndMerge: %v", err)
	}
	want := EnglishTranslationSet()
	if *got != *want {
		t.Errorf("missing overlay should yield English baseline:\n got=%+v\nwant=%+v", got, want)
	}
}

func TestLoadAndMerge_NilAfero_FallsBackToEmbed(t *testing.T) {
	// Embedded en.json is `{}` so the result must match English baseline.
	got, err := LoadAndMerge(nil, "en")
	if err != nil {
		t.Fatalf("LoadAndMerge: %v", err)
	}
	want := EnglishTranslationSet()
	if *got != *want {
		t.Errorf("nil afero + embedded en.json should yield English baseline:\n got=%+v\nwant=%+v", got, want)
	}
}

func TestLoadAndMerge_EmptyLang_ReturnsEnglish(t *testing.T) {
	got, err := LoadAndMerge(nil, "")
	if err != nil {
		t.Fatalf("LoadAndMerge: %v", err)
	}
	want := EnglishTranslationSet()
	if *got != *want {
		t.Errorf("empty lang should yield English baseline:\n got=%+v\nwant=%+v", got, want)
	}
}
