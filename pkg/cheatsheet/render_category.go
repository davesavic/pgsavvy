package cheatsheet

import (
	"strings"

	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/theme"
	"github.com/mattn/go-runewidth"
)

// Column-layout constants for the per-category two-column body.
//
//	categoryKeyColCap caps the key column width so a pathologically long
//	key grows only to the cap (the description then truncates) instead of
//	pushing every row past cheatsheetMaxCols (layout.go:1213 == 60).
//	categoryKeyGap is the fixed gap between the key column and the
//	description. The glyph slot is a single reserved cell on every row
//	(generator.go:26-36 glyphs are multibyte, so all width math uses
//	runewidth.StringWidth, never len()).
const (
	categoryKeyColCap = 30
	categoryKeyGap    = 2
	categoryGlyphSlot = 1

	// cheatsheetMaxCols mirrors the popup inner-width cap that is the source
	// of truth in orchestrator/layout.go:1213 (unexported there). A row is
	// rendered to fit within this width; descriptions truncate before it.
	cheatsheetMaxCols = 60

	// ansiBold is the SGR introducer that bolds a header line. Headers are
	// always bolded so they stand out from plain binding rows even under a
	// theme that sets no header colour; gocui (OutputTrue) lifts the inline
	// SGR to per-cell attributes.
	ansiBold = "\x1b[1m"
)

// styleHeader renders a scope/mode header line so it reads as a distinct block
// above its rows: always bold, plus the theme foreground colour resolved from
// style when one maps. A nil style (theme element unset) or an unmappable
// colour token still bolds the text. Mirrors the inline-SGR approach used by
// grid/cells.go and status_bar.go.
func styleHeader(text string, style *theme.Style) string {
	var sgr strings.Builder
	sgr.WriteString(ansiBold)
	if style != nil {
		sgr.WriteString(headerFgSGR(style.Fg))
	}
	return sgr.String() + text + theme.AnsiReset
}

// headerFgSGR resolves a foreground colour token (named or "#rrggbb" hex) to
// its SGR escape, or "" when it maps to neither — same fallback policy as the
// grid/orchestrator tinters.
func headerFgSGR(fg string) string {
	if code := theme.AnsiFgSGR(fg); code != "" {
		return code
	}
	return theme.AnsiFgHexSGR(fg)
}

// RenderCategory produces ONE tab-body string for a single CategoryView.
//
// A zero-row CategoryView (only the always-on CategoryGeneral can be empty,
// per Categorize) renders the tr.CheatsheetEmpty sentinel so the always-on
// General tab is never blank. This supersedes the stale "empty category →
// empty string" edge under the always-on-General reconciliation.
//
// Otherwise the body is: the CurrentScope partition (if any rows) under the
// tr.CheatsheetCurrentScopeTab sub-header, then the Global partition (if any
// rows) under tr.CheatsheetGlobalTab — each partition split into per-Mode
// sub-groups (modeLabel) of aligned two-column rows. The glyph legend
// (tr.CheatsheetLegend) is the single last line of the body.
//
// A nil tr collapses to "" — mirrors the old Render nil-tr contract
// (render.go:31).
func RenderCategory(cv CategoryView, tr *i18n.TranslationSet) string {
	if tr == nil {
		return ""
	}

	var b strings.Builder
	if categoryRowCount(cv) == 0 {
		b.WriteString(tr.CheatsheetEmpty)
		b.WriteByte('\n')
		b.WriteString(tr.CheatsheetLegend)
		return b.String()
	}

	keyW := categoryKeyWidth(cv)
	writeCategoryScope(&b, tr.CheatsheetCurrentScopeTab, cv.CurrentScope, keyW)
	writeCategoryScope(&b, tr.CheatsheetGlobalTab, cv.Global, keyW)

	b.WriteString(tr.CheatsheetLegend)
	return b.String()
}

// writeCategoryScope emits a scope sub-header followed by every ModeView's
// per-mode sub-group of aligned rows. Scope and mode headers are colour-styled
// (styleHeader) so each section reads as a distinct block from the plain rows
// below it — this works in every category, including the common single-mode
// scopes where a between-modes rule would never appear. A scope with zero
// views is skipped so no orphaned sub-header is painted.
func writeCategoryScope(b *strings.Builder, heading string, views []ModeView, keyW int) {
	if len(views) == 0 {
		return
	}
	th := theme.Current()
	b.WriteString(styleHeader(heading, th.InfoFg))
	b.WriteByte('\n')
	for _, v := range views {
		b.WriteString(styleHeader(modeLabel(v.Mode), th.KeywordFg))
		b.WriteByte('\n')
		for _, s := range v.Sections {
			for _, r := range s.Rows {
				b.WriteString(renderCategoryRow(r, keyW))
				b.WriteByte('\n')
			}
		}
	}
}

// renderCategoryRow renders a single aligned row: a fixed 1-cell glyph slot
// (filled only for non-default sources), the key left-padded to keyW, a fixed
// gap, then the description truncated with an ellipsis if it would overrun
// cheatsheetMaxCols. Single line, no trailing newline.
func renderCategoryRow(r Row, keyW int) string {
	var b strings.Builder
	b.WriteString(glyphSlot(r))

	key := runewidth.Truncate(r.Key, keyW, "…")
	b.WriteString(key)
	b.WriteString(strings.Repeat(" ", keyW-runewidth.StringWidth(key)))
	b.WriteString(strings.Repeat(" ", categoryKeyGap))

	used := categoryGlyphSlot + keyW + categoryKeyGap
	remaining := cheatsheetMaxCols - used
	b.WriteString(runewidth.Truncate(r.Description, remaining, "…"))
	return b.String()
}

// glyphSlot returns the fixed 1-cell glyph prefix: the source glyph for
// non-default sources, a single space for ShippedDefault (so descriptions on
// default and overridden rows still share one column — no '·' is printed).
func glyphSlot(r Row) string {
	if r.Glyph == GlyphDefault {
		return " "
	}
	return string(r.Glyph)
}

// categoryKeyWidth is the shared key-column width for a CategoryView: the max
// key display width across all its rows, capped at categoryKeyColCap.
func categoryKeyWidth(cv CategoryView) int {
	w := 0
	for _, mv := range append(append([]ModeView{}, cv.CurrentScope...), cv.Global...) {
		for _, s := range mv.Sections {
			for _, r := range s.Rows {
				if kw := runewidth.StringWidth(r.Key); kw > w {
					w = kw
				}
			}
		}
	}
	if w > categoryKeyColCap {
		return categoryKeyColCap
	}
	return w
}

// categoryRowCount totals the binding rows across both scope partitions.
func categoryRowCount(cv CategoryView) int {
	n := 0
	for _, mv := range append(append([]ModeView{}, cv.CurrentScope...), cv.Global...) {
		for _, s := range mv.Sections {
			n += len(s.Rows)
		}
	}
	return n
}
