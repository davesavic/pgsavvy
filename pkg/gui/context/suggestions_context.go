package context

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/davesavic/pgsavvy/pkg/gui/editor"
	"github.com/davesavic/pgsavvy/pkg/gui/grid"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/theme"
)

// suggestionGlyph maps a SuggestionKind to a single-rune leading glyph
// rendered before the name. The glyph set is plain ASCII (Design D7) so
// it renders in any terminal without font/width surprises:
//
//	column   '@'   table   '#'   view '%'   function '&'
//	keyword  '!'   history ':'   snippet '+'
//
// The zero value ("" / unkinded) returns ' ' so an unkinded row that
// still carries a Detail/annotation keeps the name column aligned with
// glyphed rows (a true bare-name suggestion takes the Display fallback
// path and never reaches here).
func suggestionGlyph(k editor.SuggestionKind) rune {
	switch k {
	case editor.KindColumn:
		return '@'
	case editor.KindTable:
		return '#'
	case editor.KindView:
		return '%'
	case editor.KindFunction:
		return '&'
	case editor.KindKeyword:
		return '!'
	case editor.KindHistory:
		return ':'
	case editor.KindSnippet:
		return '+'
	default:
		return ' '
	}
}

// detailTokenColor names the theme colour used to tint the detail
// column (type/kind text and PK/FK/NN annotation badges). It is wrapped
// onto the already-sanitized detail text AFTER composition so the SGR
// is never fed back through SanitizeCellEscapes (Design D4).
const detailTokenColor = "cyan"

// suggestionsVisibleMax bounds the number of suggestion rows rendered
// in the popup body. Excess suggestions remain in state and can be
// reached by scrolling Selected past the visible window (the renderer
// keeps Selected on-screen by sliding the window).
const suggestionsVisibleMax = 8

// SuggestionsContext renders the floating completion popup driven by
// the editor's completion Engine. Owns the popup state machine
// (visibility, selection cursor, anchor position) and emits the body
// text on HandleRender; the orchestrator owns view sizing / anchor
// placement (Z1).
//
// Signature help: when the SELECTED suggestion is a
// function, the popup renders a DEDICATED help FOOTER line below the
// list — `name(arg type, ...) -> ReturnType` — resolved from the
// injected detailProvider. The detail provider is a sync-read cache; on
// a miss the context fires WarmFunctionDetail with an onReady that
// re-renders the popup (re-render-on-warm), so the signature appears
// once the cache warms with no manual user re-trigger. The active
// schema (from schemaProv) is the lookup key — a documented v1 limit:
// a function whose home schema differs from the rail's selected schema
// will not resolve (no cross-schema search).
type SuggestionsContext struct {
	BaseContext

	deps Deps

	visible     bool
	suggestions []editor.Suggestion
	selected    int
	anchor      editor.Position

	// detailProvider is the injected signature-help cache (sync read +
	// async warm). nil-safe: no provider => no help line.
	detailProvider editor.FunctionDetailProvider
	// schemaProv returns the active schema name used as the (schema,name)
	// lookup key. nil-safe: nil/"" schema still attempts a lookup under "".
	schemaProv func() string
}

// NewSuggestionsContext builds a SuggestionsContext bound to SUGGESTIONS.
func NewSuggestionsContext(base BaseContext, deps Deps) *SuggestionsContext {
	return &SuggestionsContext{BaseContext: base, deps: deps}
}

// SetFunctionDetailProvider injects the function-signature-help cache
// and the active-schema source used as its lookup
// key. Both are optional and nil-safe: with no provider the popup never
// renders a signature help line. Wired from the orchestrator over the
// ConnectHelper (which satisfies editor.FunctionDetailProvider) and the
// SCHEMAS rail's selected-schema accessor, mirroring the FunctionSource
// provider wiring.
func (c *SuggestionsContext) SetFunctionDetailProvider(p editor.FunctionDetailProvider, schemaProv func() string) {
	c.detailProvider = p
	c.schemaProv = schemaProv
}

// Show installs suggestions + anchor and flips the popup visible.
// An empty suggestions slice leaves the popup hidden (there is
// nothing to render). Selected resets to 0.
func (c *SuggestionsContext) Show(suggestions []editor.Suggestion, anchor editor.Position) {
	if len(suggestions) == 0 {
		c.Hide()
		return
	}
	cp := make([]editor.Suggestion, len(suggestions))
	copy(cp, suggestions)
	c.suggestions = cp
	c.selected = 0
	c.anchor = anchor
	c.visible = true
}

// Hide clears the popup. The state (suggestions, selected) is dropped
// so the next Show starts fresh.
func (c *SuggestionsContext) Hide() {
	c.visible = false
	c.suggestions = nil
	c.selected = 0
	c.anchor = editor.Position{}
}

// IsVisible reports whether the popup should be rendered.
func (c *SuggestionsContext) IsVisible() bool { return c.visible }

// Next advances the selection cursor, wrapping at the bottom. No-op
// when the popup is hidden or has no suggestions.
func (c *SuggestionsContext) Next() {
	n := len(c.suggestions)
	if !c.visible || n == 0 {
		return
	}
	c.selected = (c.selected + 1) % n
}

// Prev reverses the selection cursor, wrapping at the top. No-op
// when the popup is hidden or has no suggestions.
func (c *SuggestionsContext) Prev() {
	n := len(c.suggestions)
	if !c.visible || n == 0 {
		return
	}
	c.selected--
	if c.selected < 0 {
		c.selected = n - 1
	}
}

// Selected returns the current cursor index. -1 when hidden / empty.
func (c *SuggestionsContext) Selected() int {
	if !c.visible || len(c.suggestions) == 0 {
		return -1
	}
	return c.selected
}

// Suggestions returns a copy of the current suggestion list for
// callers that need to inspect / audit the popup contents.
func (c *SuggestionsContext) Suggestions() []editor.Suggestion {
	if len(c.suggestions) == 0 {
		return nil
	}
	cp := make([]editor.Suggestion, len(c.suggestions))
	copy(cp, c.suggestions)
	return cp
}

// Anchor returns the editor position the popup is anchored to. Zero
// Position when hidden.
func (c *SuggestionsContext) Anchor() editor.Position { return c.anchor }

// Accept returns the currently-selected suggestion and hides the popup.
// Returns (_, false) when the popup is hidden, has no suggestions, or the
// cursor sits out of range.
//
// Sanitization branches on Kind. For a non-snippet suggestion the inserted
// text is the bare name, routed through editor.SanitizeText (strips control
// AND newline/tab bytes so a completion insert stays a single contiguous
// token). For a snippet (Kind==KindSnippet) the inserted text is the Body
// — a deliberately multi-line, indented expansion — so the Body is routed
// through editor.SanitizeSnippetText, which strips control bytes but
// PRESERVES '\n' and '\t'. Text (the snippet name) still gets SanitizeText;
// Display is left untouched (rendered through the escape sanitizer at render
// time). The controller's acceptSuggestion reads Body for the snippet branch.
func (c *SuggestionsContext) Accept() (editor.Suggestion, bool) {
	if !c.visible {
		return editor.Suggestion{}, false
	}
	if len(c.suggestions) == 0 {
		return editor.Suggestion{}, false
	}
	if c.selected < 0 || c.selected >= len(c.suggestions) {
		return editor.Suggestion{}, false
	}
	s := c.suggestions[c.selected]
	s.Text = editor.SanitizeText(s.Text)
	if s.Kind == editor.KindSnippet {
		s.Body = editor.SanitizeSnippetText(s.Body)
	}
	c.Hide()
	return s, true
}

// OnCursorMoved is the integration hook the vim editor controller
// calls on any motion / insert handler after the cursor moved. It
// distinguishes a typing-advance within the active identifier (the
// cursor stays on the anchor line at or after the anchor column —
// keep the popup so the controller can refilter it in place) from
// navigation that leaves the identifier (different line, or the
// cursor retreated before the anchor column — dismiss, vim's omni-
// complete behaviour). No-op when hidden.
//
// Thread-safety: the single gocui main loop drives every cursor move
// and Trigger synchronously, so visible / anchor are never read here
// concurrently with a Show/Hide on another goroutine.
func (c *SuggestionsContext) OnCursorMoved(pos editor.Position) {
	if !c.visible {
		return
	}
	if pos.Line == c.anchor.Line && pos.Col >= c.anchor.Col {
		return
	}
	c.Hide()
}

// HandleRender writes the popup body via deps.GuiDriver.SetContent.
// No-op when hidden or driver-less. Suggestion.Display is routed
// through grid.SanitizeCellEscapes so untrusted server text cannot
// hijack the terminal.
//
// When the SELECTED suggestion is a function, a dedicated signature
// help FOOTER line is appended below the list. The
// signature is resolved synchronously from the detail provider; on a
// cold miss the render emits the list WITHOUT a footer and fires
// WarmFunctionDetail with an onReady that re-renders, so the footer
// appears once the cache warms (re-render-on-warm).
func (c *SuggestionsContext) HandleRender() error {
	if !c.visible || len(c.suggestions) == 0 {
		return nil
	}
	body := formatSuggestionsBody(c.suggestions, c.selected, suggestionsVisibleMax)
	if footer := c.signatureFooter(); footer != "" {
		body += "\n" + footer
	}
	viewName := c.GetViewName()
	writeView(c.deps, func() error {
		return c.deps.GuiDriver.SetContent(viewName, body)
	})
	return nil
}

// signatureFooter returns the signature help line for the currently
// selected suggestion, or "" when there is no help line to show: no
// provider wired, the selection is out of range, the selected
// suggestion is not a function, or the function detail is not (yet)
// cached. On a cache miss it fires a single WarmFunctionDetail whose
// onReady re-renders the popup so the footer materialises once the
// cache warms — the user does not re-trigger completion. onReady is
// guaranteed to land on the UI loop by the provider's UI scheduler,
// so it calls HandleRender directly.
//
// The (schema, name) key uses the active schema from schemaProv (a
// documented v1 limit — see the type doc); name is the suggestion's
// insert Text. A permanently-cold key (provider keeps returning
// found=false) simply keeps the footer empty: the warm is idempotent
// per key, so no spinner-lock and no repeated re-render storm.
func (c *SuggestionsContext) signatureFooter() string {
	if c.detailProvider == nil {
		return ""
	}
	if c.selected < 0 || c.selected >= len(c.suggestions) {
		return ""
	}
	s := c.suggestions[c.selected]
	if s.Kind != editor.KindFunction {
		return ""
	}
	name := s.Text
	if name == "" {
		return ""
	}
	schema := ""
	if c.schemaProv != nil {
		schema = c.schemaProv()
	}
	details, ok := c.detailProvider.FunctionDetail(schema, name)
	if !ok {
		c.detailProvider.WarmFunctionDetail(schema, name, func() {
			_ = c.HandleRender()
		})
		return ""
	}
	return formatSignatureLine(name, details)
}

// formatSuggestionsBody renders one line per visible suggestion.
// Selected row gets the "> " marker; others get "  " so column
// alignment stays stable. The visible window slides to keep selected
// inside [start, start+visibleMax).
//
// Each typed row composes as "<marker><glyph> <name><pad><detail>",
// where the name and DB-derived detail/FK text are sanitized BEFORE
// composition (Design D4) and the glyph/marker/padding/theme SGR are
// added AFTER — the composed SGR row is never re-sanitized, so our own
// colour codes survive while a crafted escape in a column name is
// stripped. The name column is right-padded to the widest sanitized
// name (rune count, Design D5) in the visible window so detail columns
// align for ASCII names; wide CJK/emoji alignment is a documented
// non-goal (rune-count only).
//
// A suggestion with an empty Detail, an unkinded Kind, and no PK/FK/NN
// annotation falls back to its sanitized Display rendered exactly as
// the pre-change behaviour (Design D2/D6).
func formatSuggestionsBody(suggestions []editor.Suggestion, selected, visibleMax int) string {
	if visibleMax <= 0 {
		visibleMax = len(suggestions)
	}
	n := len(suggestions)
	start := 0
	if n > visibleMax {
		// Slide the window so selected stays on-screen.
		if selected >= visibleMax {
			start = selected - visibleMax + 1
		}
		if start+visibleMax > n {
			start = n - visibleMax
		}
		if start < 0 {
			start = 0
		}
	}
	end := min(start+visibleMax, n)

	// First pass: sanitize each visible name and find the widest one
	// (rune count) so the detail column aligns. Names are sanitized
	// here, before any glyph/marker/SGR/padding is composed (Design D4).
	names := make([]string, end-start)
	nameWidth := 0
	for i := start; i < end; i++ {
		names[i-start] = grid.SanitizeCellEscapes(suggestionName(suggestions[i]))
		if w := utf8.RuneCountInString(names[i-start]); w > nameWidth {
			nameWidth = w
		}
	}

	var sb strings.Builder
	for i := start; i < end; i++ {
		if i > start {
			sb.WriteByte('\n')
		}
		if i == selected {
			sb.WriteString("> ")
		} else {
			sb.WriteString("  ")
		}
		sb.WriteString(composeSuggestionRow(suggestions[i], names[i-start], nameWidth))
	}
	return sb.String()
}

// SuggestionsRenderWidth returns the widest *rendered* row width in runes for
// the given suggestions — the content width the anchored completion popup must
// reserve so no row is clipped horizontally. It mirrors
// formatSuggestionsBody/composeSuggestionRow exactly: the 2-col "> " selection
// marker, the glyph + space prefix, the name column padded to the widest name,
// the 2-col detail gutter, and the detail tokens. The layout previously sized
// the box from len(Display)+marker alone, which omitted the glyph/space/detail
// columns and clipped wide rows ("> ! WHER" instead of "WHERE").
func SuggestionsRenderWidth(suggestions []editor.Suggestion) int {
	const markerCols = 2
	// First pass mirrors formatSuggestionsBody: sanitize each name and find
	// the widest (rune count) so typed rows' detail column aligns.
	names := make([]string, len(suggestions))
	nameWidth := 0
	for i, s := range suggestions {
		names[i] = grid.SanitizeCellEscapes(suggestionName(s))
		if w := utf8.RuneCountInString(names[i]); w > nameWidth {
			nameWidth = w
		}
	}
	maxBody := 0
	for i, s := range suggestions {
		if w := suggestionRowBodyWidth(s, names[i], nameWidth); w > maxBody {
			maxBody = w
		}
	}
	return markerCols + maxBody
}

// suggestionRowBodyWidth is the rune width of composeSuggestionRow's output
// with styling stripped. It MUST stay in lockstep with composeSuggestionRow.
func suggestionRowBodyWidth(s editor.Suggestion, sanitizedName string, nameWidth int) int {
	if isUntyped(s) {
		return utf8.RuneCountInString(grid.SanitizeCellEscapes(s.Display))
	}
	// glyph + space + name
	w := 2 + utf8.RuneCountInString(sanitizedName)
	detail := suggestionDetail(s)
	if detail == "" {
		return w
	}
	pad := nameWidth - utf8.RuneCountInString(sanitizedName)
	if pad < 0 {
		pad = 0
	}
	// pad + 2-col gutter + detail (suggestionDetail already sanitized its
	// tokens, so its rune count is the visible width composeSuggestionRow draws)
	return w + pad + 2 + utf8.RuneCountInString(detail)
}

// suggestionName returns the bare name to render for a suggestion: the
// insert Text when set (typed rows carry the bare identifier in Text
// and metadata in the typed fields), otherwise the Display. The caller
// sanitizes the result.
func suggestionName(s editor.Suggestion) string {
	if s.Text != "" {
		return s.Text
	}
	return s.Display
}

// isUntyped reports whether a suggestion carries no typed presentation
// metadata — empty Detail, unkinded, and no PK/FK/NN annotation. Such
// rows take the Display fallback (Design D2/D6).
func isUntyped(s editor.Suggestion) bool {
	return s.Kind == "" && s.Detail == "" && !s.IsPrimaryKey && !s.NotNull && s.FKRef == ""
}

// composeSuggestionRow builds the post-marker body for one suggestion
// from its already-sanitized name. Untyped rows fall back to the
// sanitized Display (Design D2). Typed rows render
// "<glyph> <name><pad><detail tokens>"; detail tokens are sanitized
// then theme-tinted, and the tint SGR is added here (never re-fed to
// SanitizeCellEscapes — Design D4).
//
// Match highlighting (Design D3): the bare sanitized name is
// wrapped via grid.HighlightRuneSpans BEFORE the glyph/marker prefix and
// detail suffix are added, so the "> "/"  " 2-col marker never shifts the
// highlight onto the wrong runes and the detail tokens are never
// highlighted. Padding for detail alignment is measured on the unstyled
// name (sanitizedName), so the injected SGR does not perturb column width.
func composeSuggestionRow(s editor.Suggestion, sanitizedName string, nameWidth int) string {
	if isUntyped(s) {
		return grid.SanitizeCellEscapes(s.Display)
	}

	var sb strings.Builder
	sb.WriteRune(suggestionGlyph(s.Kind))
	sb.WriteByte(' ')
	sb.WriteString(highlightName(suggestionName(s), sanitizedName, s.Matches))

	detail := suggestionDetail(s)
	if detail == "" {
		return sb.String()
	}
	// Pad the name column to the widest visible name (rune count, D5)
	// so the detail tokens align. A trailing two-space gutter separates
	// name from detail.
	pad := nameWidth - utf8.RuneCountInString(sanitizedName)
	if pad < 0 {
		pad = 0
	}
	sb.WriteString(strings.Repeat(" ", pad))
	sb.WriteString("  ")
	sb.WriteString(tint(detail, detailTokenColor))
	return sb.String()
}

// tint wraps s in the named theme foreground SGR + reset, EXCEPT under
// NO_COLOR (theme.IsMonochrome): a monochrome runtime emits s unchanged so the
// suggestion row carries no colour codes. An
// unknown/empty colour name (AnsiFgSGR == "") also returns s plain. The SGR is
// added AFTER the caller has sanitized s and is never re-fed to
// SanitizeCellEscapes (Design D4).
func tint(s, color string) string {
	if theme.IsMonochrome() {
		return s
	}
	sgr := theme.AnsiFgSGR(color)
	if sgr == "" {
		return s
	}
	return sgr + s + theme.AnsiReset
}

// highlightName wraps the matched runes of the sanitized name in the
// Search SGR (grid.HighlightRuneSpans) and returns the result. matches
// are RUNE offsets into the RAW name (Suggestion.Text); sanitized is the
// SanitizeCellEscapes'd name actually rendered. Empty/nil matches return
// sanitized unchanged (byte-identical to the no-highlight path).
//
// SanitizeCellEscapes is NOT length-preserving (it strips escape bytes),
// so a Match offset into the raw name does not map onto the sanitized
// name once an escape was removed. Mirroring grid.applyMatchHighlights'
// whole-cell fallback (review-plan Finding D, Design D8): when sanitizing
// changed the name we cannot trust the offsets, so we highlight the WHOLE
// sanitized name instead of indexing wrong runes. Otherwise each match
// rune index becomes a [i,i+1) span; grid.HighlightRuneSpans clamps/drops
// any out-of-range span and merges adjacencies per rune, never slicing
// mid-rune or panicking.
func highlightName(rawName, sanitized string, matches []int) string {
	if len(matches) == 0 {
		return sanitized
	}
	// No-color (NO_COLOR): suppress the match-highlight SGR too so the row
	// renders as plain text. The matched runes
	// are still present — only the highlight tint is dropped.
	if theme.IsMonochrome() {
		return sanitized
	}
	if rawName != sanitized {
		// Offsets are no longer valid after escape stripping: whole-name
		// fallback (highlight every rune of the sanitized name).
		return grid.HighlightRuneSpans(sanitized, [][2]int{{0, utf8.RuneCountInString(sanitized)}})
	}
	spans := make([][2]int, 0, len(matches))
	for _, m := range matches {
		spans = append(spans, [2]int{m, m + 1})
	}
	return grid.HighlightRuneSpans(sanitized, spans)
}

// signatureTokenColor names the theme colour used to tint the
// signature help footer. It is applied AFTER the per-segment sanitize +
// compose so the SGR is never fed back through SanitizeCellEscapes
// (Design D4, adopted verbatim per review-plan Finding E).
const signatureTokenColor = "cyan"

// formatSignatureLine renders the dedicated signature help footer for a
// selected function suggestion from its cached FunctionDetail(s):
//
//	name(argname argtype, ...) -> ReturnType
//
// For OVERLOADS (len > 1) it renders the FIRST overload and appends a
// "  (+N overloads)" count; cycling between overloads is out of scope
// (deferred). An empty details slice or an entry with no args renders
// "name() -> ReturnType". A missing ReturnType drops the "-> ..." tail.
//
// Security (review-plan Finding E, Design D4): every server-derived
// segment — each arg name, each arg type, the return type — is routed
// through grid.SanitizeCellEscapes INDIVIDUALLY before the "name(...)"
// string is composed, and the theme tint SGR is wrapped on AFTER. The
// composed (SGR-bearing) string is NEVER re-sanitized, so a crafted
// escape inside an arg type is stripped while our own colour survives.
// `name` is the suggestion's insert Text (a bare identifier), but it is
// sanitized here too for defence in depth.
//
// Returns "" when details is empty (no help line for an uncached/empty
// detail — graceful absence, not a placeholder).
func formatSignatureLine(name string, details []models.FunctionDetail) string {
	if len(details) == 0 {
		return ""
	}
	first := details[0]

	var sb strings.Builder
	sb.WriteString(grid.SanitizeCellEscapes(name))
	sb.WriteByte('(')
	for i, arg := range first.Args {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(formatSignatureArg(arg))
	}
	sb.WriteByte(')')
	if rt := grid.SanitizeCellEscapes(first.ReturnType); rt != "" {
		sb.WriteString(" -> ")
		sb.WriteString(rt)
	}
	if n := len(details) - 1; n > 0 {
		sb.WriteString("  (+")
		sb.WriteString(strconv.Itoa(n))
		if n == 1 {
			sb.WriteString(" overload)")
		} else {
			sb.WriteString(" overloads)")
		}
	}

	return tint(sb.String(), signatureTokenColor)
}

// formatSignatureArg renders one FunctionArg as "name type" with an
// optional Mode prefix for OUT / INOUT / VARIADIC (IN is the implicit
// default and gets no prefix). Both the arg name and type are
// server-derived, so each is sanitized individually here BEFORE the
// composed segment is returned (the caller never re-sanitizes the
// joined signature — Design D4). An empty name renders just the type;
// an empty type renders just the name.
func formatSignatureArg(arg models.FunctionArg) string {
	parts := make([]string, 0, 3)
	if m := arg.Mode; m == "OUT" || m == "INOUT" || m == "VARIADIC" {
		parts = append(parts, m)
	}
	if nm := grid.SanitizeCellEscapes(arg.Name); nm != "" {
		parts = append(parts, nm)
	}
	if tp := grid.SanitizeCellEscapes(arg.Type); tp != "" {
		parts = append(parts, tp)
	}
	return strings.Join(parts, " ")
}

// suggestionDetail assembles the (already-sanitized) detail token string
// for a typed suggestion: the Detail text (type/kind), then "PK" when a
// primary key, "-> <FKRef>" when a foreign key, "NN" when NOT NULL.
// Detail and FKRef are DB-derived and sanitized here, BEFORE any tint
// SGR is applied by the caller (Design D4). Returns "" when no tokens
// apply.
func suggestionDetail(s editor.Suggestion) string {
	tokens := make([]string, 0, 4)
	if d := grid.SanitizeCellEscapes(s.Detail); d != "" {
		tokens = append(tokens, d)
	}
	if s.IsPrimaryKey {
		tokens = append(tokens, "PK")
	}
	if s.FKRef != "" {
		tokens = append(tokens, "-> "+grid.SanitizeCellEscapes(s.FKRef))
	}
	if s.NotNull {
		tokens = append(tokens, "NN")
	}
	return strings.Join(tokens, " ")
}
