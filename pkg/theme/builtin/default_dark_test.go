package builtin

import (
	"reflect"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/config"
)

// TestDefaultDark_AllStringFieldsNonEmpty walks every exported string field of
// the returned ThemeConfig and asserts each is non-empty. This is a drift
// guard: when a new field is added to config.ThemeConfig, this test fails
// until DefaultDark is updated to supply a default for it.
func TestDefaultDark_AllStringFieldsNonEmpty(t *testing.T) {
	cfg := DefaultDark()
	if cfg == nil {
		t.Fatal("DefaultDark returned nil")
	}

	v := reflect.ValueOf(*cfg)
	tp := v.Type()

	if v.NumField() == 0 {
		t.Fatal("ThemeConfig has zero fields; reflect walk is meaningless")
	}

	for i := 0; i < v.NumField(); i++ {
		f := tp.Field(i)
		if !f.IsExported() {
			continue
		}
		fv := v.Field(i)
		if fv.Kind() != reflect.String {
			continue
		}
		if fv.String() == "" {
			t.Errorf("DefaultDark left exported string field %q empty", f.Name)
		}
	}
}

// TestDefaultDark_MatchesGetDefaultConfig is the config<->builtin invariant: the
// Theme block returned by config.GetDefaultConfig() must equal *DefaultDark()
// field-for-field. T1's bootstrap wiring calls theme.Apply(&cfg.Theme) directly
// (no overlay), relying on LoadUserConfig overlaying partial user YAML onto this
// full baseline; if the two drift, unset user fields would reach Apply as "" and
// render untinted. This test fails if a field is added to one but not the other.
func TestDefaultDark_MatchesGetDefaultConfig(t *testing.T) {
	want := *DefaultDark()
	got := config.GetDefaultConfig().Theme

	wv := reflect.ValueOf(want)
	gv := reflect.ValueOf(got)
	tp := wv.Type()

	if wv.NumField() == 0 {
		t.Fatal("ThemeConfig has zero fields; field-for-field walk is meaningless")
	}

	for i := 0; i < wv.NumField(); i++ {
		f := tp.Field(i)
		if !f.IsExported() || wv.Field(i).Kind() != reflect.String {
			continue
		}
		if w, g := wv.Field(i).String(), gv.Field(i).String(); w != g {
			t.Errorf("Theme.%s: GetDefaultConfig()=%q, DefaultDark()=%q (config and builtin drifted)", f.Name, g, w)
		}
	}
}
