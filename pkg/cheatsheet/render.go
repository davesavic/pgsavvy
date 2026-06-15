package cheatsheet

import (
	"strings"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/i18n"
)

// Render produces the deterministic, terminal-styling-free text dump of
// out for the cheatsheet popup. Layout:
//
//	<CheatsheetTitle>
//	<CheatsheetLegend>
//
//	== <CheatsheetCurrentScopeTab>: <scope label> ==
//	-- <ModeLabel> --
//	[<tag>]                              (omitted when tag is empty)
//	  <glyph> <key>  <description>
//	  ...
//
//	== <CheatsheetGlobalTab> ==
//	...
//
// The empty-trie path renders title + legend + <CheatsheetEmpty>
// sentinel so the popup is never blank.
//
// A nil tr collapses to an empty string — the caller (CheatsheetContext)
// nil-checks tr before invoking Render.
func Render(out Output, tr *i18n.TranslationSet, scopeLabel string) string {
	if tr == nil {
		return ""
	}

	var b strings.Builder
	b.WriteString(tr.CheatsheetTitle)
	b.WriteByte('\n')
	b.WriteString(tr.CheatsheetLegend)
	b.WriteByte('\n')

	if len(out.CurrentScope) == 0 && len(out.Global) == 0 {
		b.WriteByte('\n')
		b.WriteString(tr.CheatsheetEmpty)
		return b.String()
	}

	writeScope(&b, tr.CheatsheetCurrentScopeTab+": "+scopeLabel, out.CurrentScope)
	writeScope(&b, tr.CheatsheetGlobalTab, out.Global)

	return b.String()
}

// writeScope emits a "== <heading> ==" banner followed by every ModeView
// in views. A view with zero sections is skipped (defensive — Generate
// already filters empty modes, but Render must not paint orphaned
// banners if the input is malformed).
func writeScope(b *strings.Builder, heading string, views []ModeView) {
	if len(views) == 0 {
		return
	}
	b.WriteString("\n\n== ")
	b.WriteString(heading)
	b.WriteString(" ==")
	for _, v := range views {
		if len(v.Sections) == 0 {
			continue
		}
		b.WriteString("\n-- ")
		b.WriteString(modeLabel(v.Mode))
		b.WriteString(" --")
		for _, s := range v.Sections {
			if s.Tag != "" {
				b.WriteString("\n[")
				b.WriteString(s.Tag)
				b.WriteByte(']')
			}
			for _, r := range s.Rows {
				b.WriteByte('\n')
				b.WriteString("  ")
				b.WriteRune(r.Glyph)
				b.WriteByte(' ')
				b.WriteString(r.Key)
				b.WriteString("  ")
				b.WriteString(r.Description)
			}
		}
	}
}

// ScopeLabel returns the display string for scope inside the
// CheatsheetCurrentScopeTab banner. The literal scope token "all"
// (covering every non-popup context) is replaced with
// Tr.CheatsheetScopeAllLabel for clarity; every other key passes
// through as-is.
func ScopeLabel(scope types.ContextKey, tr *i18n.TranslationSet) string {
	if scope == "all" && tr != nil {
		return tr.CheatsheetScopeAllLabel
	}
	return scope.Display()
}
