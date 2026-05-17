package i18n

import (
	"encoding/json"
	"io/fs"
	"strings"
	"testing"
)

func TestEmbeddedTranslations_AllFilesParseIntoTranslationSet(t *testing.T) {
	entries, err := fs.ReadDir(embeddedTranslations, "translations")
	if err != nil {
		t.Fatalf("read embedded translations dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("no embedded translations found; expected at least en.json")
	}

	sawEN := false
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		if name == "en.json" {
			sawEN = true
		}

		data, err := fs.ReadFile(embeddedTranslations, "translations/"+name)
		if err != nil {
			t.Errorf("read %s: %v", name, err)
			continue
		}
		var set TranslationSet
		if err := json.Unmarshal(data, &set); err != nil {
			t.Errorf("unmarshal %s into TranslationSet: %v", name, err)
		}
	}

	if !sawEN {
		t.Errorf("embedded translations missing en.json; required as English placeholder")
	}
}
