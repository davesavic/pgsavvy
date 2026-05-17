package builtin

import (
	"reflect"
	"testing"
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
