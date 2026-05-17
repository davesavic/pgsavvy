package style

import "github.com/davesavic/dbsavvy/pkg/gui/types"

// TextStyle is a value-typed, immutable description of how a fragment of
// text should be rendered. Every Set* method returns a fresh TextStyle and
// leaves the receiver unchanged so call sites can safely fan out:
//
//	a := style.New().SetFg("red")
//	b := a.SetBold(true)  // a.Bold remains false
//
// TextStyle is intentionally dependency-free; the rendering layer (T10)
// translates the plain string colour fields into gocui FrameColor values
// or terminal escape sequences.
type TextStyle struct {
	Fg        string
	Bg        string
	Bold      bool
	Underline bool
	Italic    bool
}

// New returns the zero TextStyle. Provided for symmetry with the chainable
// API; equivalent to a bare TextStyle{} literal.
func New() TextStyle {
	return TextStyle{}
}

// SetFg returns a copy of s with Fg set to fg.
func (s TextStyle) SetFg(fg string) TextStyle {
	s.Fg = fg
	return s
}

// SetBg returns a copy of s with Bg set to bg.
func (s TextStyle) SetBg(bg string) TextStyle {
	s.Bg = bg
	return s
}

// SetBold returns a copy of s with Bold set to b.
func (s TextStyle) SetBold(b bool) TextStyle {
	s.Bold = b
	return s
}

// SetUnderline returns a copy of s with Underline set to u.
func (s TextStyle) SetUnderline(u bool) TextStyle {
	s.Underline = u
	return s
}

// SetItalic returns a copy of s with Italic set to i.
func (s TextStyle) SetItalic(i bool) TextStyle {
	s.Italic = i
	return s
}

// MergeStyle overlays other onto s. Non-empty string fields in other
// replace the corresponding fields in s; boolean fields are OR'd so any
// emphasis set on either operand survives. The receiver is not mutated.
// MergeStyle is safe to call with two zero values: it returns the zero
// TextStyle.
func (s TextStyle) MergeStyle(other TextStyle) TextStyle {
	if other.Fg != "" {
		s.Fg = other.Fg
	}
	if other.Bg != "" {
		s.Bg = other.Bg
	}
	s.Bold = s.Bold || other.Bold
	s.Underline = s.Underline || other.Underline
	s.Italic = s.Italic || other.Italic
	return s
}

// Sprint returns text unchanged. The full builder writes terminal escape
// sequences in a future epic; for now Sprint is a no-op so call sites can
// be written against the final API and the status bar can compose strings
// uniformly whether or not styling is applied.
func (s TextStyle) Sprint(text string) string {
	return text
}

// ToTypes projects s onto the frozen types.TextStyle surface consumed by
// ContextTreeDeps.PresentationHook. The two extra fields (Underline,
// Italic) are dropped because the hook return type does not carry them.
func (s TextStyle) ToTypes() types.TextStyle {
	return types.TextStyle{Fg: s.Fg, Bg: s.Bg, Bold: s.Bold}
}

// FromTypes lifts a types.TextStyle value into the richer style.TextStyle
// builder. Provided so call sites that receive the hook return value can
// chain further Set* calls.
func FromTypes(t types.TextStyle) TextStyle {
	return TextStyle{Fg: t.Fg, Bg: t.Bg, Bold: t.Bold}
}
