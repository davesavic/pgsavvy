package i18n

import (
	"reflect"
	"testing"
)

func TestEnglishTranslationSet_FreshAllocation(t *testing.T) {
	a := EnglishTranslationSet()
	b := EnglishTranslationSet()

	if a == b {
		t.Fatalf("EnglishTranslationSet returned the same pointer on two calls; expected fresh allocation each call")
	}

	// Mutating one must not affect the other.
	a.OpenTable = "MUTATED"
	a.Actions.RunQuery = "MUTATED"
	if b.OpenTable == "MUTATED" {
		t.Fatalf("mutation of one TranslationSet leaked into another; expected isolation")
	}
	if b.Actions.RunQuery == "MUTATED" {
		t.Fatalf("mutation of nested Actions leaked into another set; expected isolation")
	}
}

func TestEnglishTranslationSet_AllFieldsNonEmpty(t *testing.T) {
	set := EnglishTranslationSet()
	assertAllStringFieldsNonEmpty(t, reflect.ValueOf(*set), "TranslationSet")
}

func assertAllStringFieldsNonEmpty(t *testing.T, v reflect.Value, prefix string) {
	t.Helper()
	typ := v.Type()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		name := prefix + "." + typ.Field(i).Name
		switch f.Kind() {
		case reflect.String:
			if f.String() == "" {
				t.Errorf("field %s is empty; English baseline must populate every string", name)
			}
		case reflect.Struct:
			assertAllStringFieldsNonEmpty(t, f, name)
		}
	}
}
