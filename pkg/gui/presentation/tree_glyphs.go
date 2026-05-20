package presentation

// Tree glyph constants used by EXPLAIN plan rendering (and any other
// collapsible tree views). The three glyphs cover the only three states
// a node line can have:
//
//   - GlyphExpanded ("▼"): an interior node whose children are visible.
//   - GlyphCollapsed ("▶"): an interior node whose children are hidden.
//   - GlyphLeaf ("─"): a node with no children.
//
// Plain Unicode, no SGR escapes — the renderer composes coloring on top
// via theme.Style. dbsavvy-uv0.8.
const (
	GlyphExpanded  = "▼"
	GlyphCollapsed = "▶"
	GlyphLeaf      = "─"
)
