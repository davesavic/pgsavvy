package theme

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/davesavic/pgsavvy/pkg/config"
)

// ApplyUserConfig validates the colour tokens in the user's theme config,
// applies it as the active theme, and returns warnings for any tokens that do
// not name a renderable colour. It does NOT print the warnings — the caller
// (app bootstrap) decides where they surface (see epic pgsavvy-w1gh.3).
//
// theme.Apply is called directly with user (no overlay onto DefaultDark):
// config.LoadUserConfig already overlays the user's YAML onto the full
// GetDefaultConfig() baseline, so by the time a *config.ThemeConfig reaches
// here every field is populated — unset keys carry the DefaultDark default, set
// keys carry the user value. The config<->builtin invariant test
// (pgsavvy-w1gh.2) guards that baseline. There is therefore no err to return:
// Apply only errors on a nil cfg and user is never nil here.
//
// Validation reuses parseStyle — the SAME tokenizer the renderer uses — so the
// tokens it classifies are exactly the ones rendered as colours: the Fg token
// and the 'on' Bg token. Attribute keywords (bold/underline/italic) and stray
// trailing barewords never land in Fg/Bg, so they are never classified and
// cannot produce a false-positive warning (e.g. "blue notacolor" renders blue
// and warns nothing). Only a genuinely unrenderable Fg/Bg yields a warning.
func ApplyUserConfig(user *config.ThemeConfig) (warnings []string) {
	v := reflect.ValueOf(*user)
	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() || v.Field(i).Kind() != reflect.String {
			continue
		}
		style := parseStyle(v.Field(i).String())
		field := yamlFieldName(f)
		if w, ok := unknownColorWarning(field, style.Fg); ok {
			warnings = append(warnings, w)
		}
		if w, ok := unknownColorWarning(field, style.Bg); ok {
			warnings = append(warnings, w)
		}
	}
	_ = Apply(user)
	return warnings
}

// unknownColorWarning returns a warning when token is a non-empty, unrenderable
// colour for the given yaml field. The empty token classifies as Empty (not
// Unknown), so an unset Fg/Bg never warns.
func unknownColorWarning(field, token string) (string, bool) {
	if k, _ := ClassifyColor(token); k == Unknown {
		return fmt.Sprintf("theme.%s: unknown color %q", field, token), true
	}
	return "", false
}

// yamlFieldName returns the yaml tag name for a struct field (without options
// such as ",omitempty"), falling back to the Go field name when no tag is set.
func yamlFieldName(f reflect.StructField) string {
	tag := f.Tag.Get("yaml")
	if i := strings.IndexByte(tag, ','); i >= 0 {
		tag = tag[:i]
	}
	if tag == "" {
		return f.Name
	}
	return tag
}
