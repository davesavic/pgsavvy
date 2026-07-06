package config

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// themeKeyToken matches a backtick-wrapped theme color key as written in the
// docs (e.g. `keyword`, `table_header`, `popup_border`, `cur_search`).
var themeKeyToken = regexp.MustCompile("`([a-z][a-z0-9_]*_(?:fg|bg|border|highlight|search))`")

// currentThemeYAMLKeys returns the set of yaml keys currently declared on
// ThemeConfig — the single source of truth for which theme knobs exist.
func currentThemeYAMLKeys() map[string]bool {
	keys := map[string]bool{}
	tp := reflect.TypeOf(ThemeConfig{})
	for i := 0; i < tp.NumField(); i++ {
		tag := tp.Field(i).Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		keys[strings.Split(tag, ",")[0]] = true
	}
	return keys
}

// TestInstallDocsThemeKeysAreValid is the doc-consistency gate for the ialt
// epic: every theme color key named in docs/INSTALL.md must still be a field
// on ThemeConfig. Reintroducing a trimmed key (or advertising a non-existent
// one) into the docs fails here instead of silently promising a no-op knob.
func TestInstallDocsThemeKeysAreValid(t *testing.T) {
	const docPath = "../../docs/INSTALL.md"
	data, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read %s: %v", docPath, err)
	}
	valid := currentThemeYAMLKeys()
	for _, m := range themeKeyToken.FindAllStringSubmatch(string(data), -1) {
		key := m[1]
		if !valid[key] {
			t.Errorf("docs/INSTALL.md references theme key %q, which is not a current ThemeConfig field", key)
		}
	}
}
