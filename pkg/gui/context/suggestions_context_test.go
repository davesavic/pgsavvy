package context

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/davesavic/pgsavvy/pkg/gui/editor"
	"github.com/davesavic/pgsavvy/pkg/gui/grid"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/theme"
)

func newTestSuggestions(drv types.GuiDriver) *SuggestionsContext {
	base := NewBaseContext(BaseContextOpts{
		Key:      types.SUGGESTIONS,
		ViewName: string(types.SUGGESTIONS),
		Kind:     types.TEMPORARY_POPUP,
	})
	deps := types.ContextTreeDeps{GuiDriver: drv}
	return NewSuggestionsContext(base, deps)
}

func TestSuggestionsContext_ShowHide_Visibility(t *testing.T) {
	c := newTestSuggestions(nil)
	if c.IsVisible() {
		t.Fatal("zero-value context IsVisible = true; want false")
	}
	c.Show([]editor.Suggestion{{Text: "a", Display: "a"}}, editor.Position{Line: 1, Col: 2})
	if !c.IsVisible() {
		t.Fatal("after Show IsVisible = false; want true")
	}
	c.Hide()
	if c.IsVisible() {
		t.Fatal("after Hide IsVisible = true; want false")
	}
}

func TestSuggestionsContext_Show_EmptyLeavesHidden(t *testing.T) {
	c := newTestSuggestions(nil)
	c.Show(nil, editor.Position{})
	if c.IsVisible() {
		t.Fatal("Show(nil) flipped visible; want hidden")
	}
	c.Show([]editor.Suggestion{}, editor.Position{})
	if c.IsVisible() {
		t.Fatal("Show(empty slice) flipped visible; want hidden")
	}
}

func TestSuggestionsContext_NextPrev_Wrap(t *testing.T) {
	c := newTestSuggestions(nil)
	c.Show([]editor.Suggestion{
		{Text: "a", Display: "a"},
		{Text: "b", Display: "b"},
		{Text: "c", Display: "c"},
	}, editor.Position{})

	if got := c.Selected(); got != 0 {
		t.Fatalf("initial Selected = %d; want 0", got)
	}
	c.Next()
	if got := c.Selected(); got != 1 {
		t.Fatalf("after Next Selected = %d; want 1", got)
	}
	c.Next()
	c.Next() // wraps 2 -> 0
	if got := c.Selected(); got != 0 {
		t.Fatalf("after wrap-around Next Selected = %d; want 0", got)
	}
	c.Prev() // 0 -> 2
	if got := c.Selected(); got != 2 {
		t.Fatalf("after wrap Prev Selected = %d; want 2", got)
	}
}

func TestSuggestionsContext_NextPrev_HiddenNoop(t *testing.T) {
	c := newTestSuggestions(nil)
	c.Next()
	c.Prev()
	if c.IsVisible() {
		t.Fatal("Next/Prev on hidden context should not flip visibility")
	}
	if got := c.Selected(); got != -1 {
		t.Errorf("Selected on hidden = %d; want -1", got)
	}
}

func TestSuggestionsContext_Accept_Visible(t *testing.T) {
	c := newTestSuggestions(nil)
	c.Show([]editor.Suggestion{
		{Text: "first", Display: "first"},
		{Text: "second", Display: "second"},
	}, editor.Position{})
	c.Next() // selected=1
	got, ok := c.Accept()
	if !ok {
		t.Fatal("Accept on visible returned ok=false")
	}
	if got.Text != "second" {
		t.Errorf("Accept Text = %q; want %q", got.Text, "second")
	}
	if c.IsVisible() {
		t.Error("Accept should hide popup; still visible")
	}
}

func TestSuggestionsContext_Accept_HiddenReturnsFalse(t *testing.T) {
	c := newTestSuggestions(nil)
	_, ok := c.Accept()
	if ok {
		t.Error("Accept on hidden returned ok=true; want false")
	}
}

func TestSuggestionsContext_Accept_EmptySuggestionsReturnsFalse(t *testing.T) {
	c := newTestSuggestions(nil)
	// Hand-craft a "visible but empty" state — defensive: Show
	// refuses empty so we go through the field directly.
	c.visible = true
	c.suggestions = nil
	_, ok := c.Accept()
	if ok {
		t.Error("Accept on visible-but-empty returned ok=true; want false")
	}
}

func TestSuggestionsContext_Accept_SanitizesText(t *testing.T) {
	c := newTestSuggestions(nil)
	c.Show([]editor.Suggestion{
		{Text: "se\nle\x00ct", Display: "select"},
	}, editor.Position{})
	got, ok := c.Accept()
	if !ok {
		t.Fatal("Accept returned ok=false")
	}
	if got.Text != "select" {
		t.Errorf("Accept Text = %q; want %q (control + newline stripped)", got.Text, "select")
	}
}

func TestSuggestionsContext_OnCursorMoved_NavigationDismisses(t *testing.T) {
	c := newTestSuggestions(nil)
	c.Show([]editor.Suggestion{{Text: "a", Display: "a"}}, editor.Position{Line: 0, Col: 5})

	// Cursor retreats before the anchor column => navigation away.
	c.OnCursorMoved(editor.Position{Line: 0, Col: 4})
	if c.IsVisible() {
		t.Error("OnCursorMoved with retreating cursor did not dismiss popup")
	}

	// Cursor jumps to another line => navigation away.
	c.Show([]editor.Suggestion{{Text: "a", Display: "a"}}, editor.Position{Line: 0, Col: 5})
	c.OnCursorMoved(editor.Position{Line: 1, Col: 5})
	if c.IsVisible() {
		t.Error("OnCursorMoved to a different line did not dismiss popup")
	}
}

func TestSuggestionsContext_OnCursorMoved_TypingAdvanceKeepsPopup(t *testing.T) {
	c := newTestSuggestions(nil)
	c.Show([]editor.Suggestion{{Text: "a", Display: "a"}}, editor.Position{Line: 0, Col: 5})

	// Cursor advances on the anchor line (typing into the identifier).
	c.OnCursorMoved(editor.Position{Line: 0, Col: 6})
	if !c.IsVisible() {
		t.Error("OnCursorMoved with typing-advance dismissed popup; want kept")
	}
}

func TestSuggestionsContext_OnCursorMoved_HiddenNoop(t *testing.T) {
	c := newTestSuggestions(nil)
	c.OnCursorMoved(editor.Position{Line: 0, Col: 0})
	if c.IsVisible() {
		t.Error("OnCursorMoved on hidden popup flipped visible")
	}
}

func TestSuggestionsContext_HandleRender_HiddenNoWrite(t *testing.T) {
	drv := &captureDriver{}
	c := newTestSuggestions(drv)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.writes != 0 {
		t.Errorf("SetContent called %d times when hidden; want 0", drv.writes)
	}
}

func TestSuggestionsContext_HandleRender_VisibleEmitsBody(t *testing.T) {
	drv := &captureDriver{}
	c := newTestSuggestions(drv)
	c.Show([]editor.Suggestion{
		{Text: "select", Display: "select"},
		{Text: "from", Display: "from"},
		{Text: "where", Display: "where"},
	}, editor.Position{})
	c.Next() // selected = 1 ("from")
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.writes != 1 {
		t.Fatalf("writes = %d; want 1", drv.writes)
	}
	if drv.lastView != string(types.SUGGESTIONS) {
		t.Errorf("view = %q; want %q", drv.lastView, string(types.SUGGESTIONS))
	}
	body := drv.lastContent
	for _, want := range []string{"select", "from", "where"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q; got %q", want, body)
		}
	}
	// Marker row must be on the selected ("from") line.
	lines := strings.Split(body, "\n")
	var fromLine, selectLine, whereLine string
	for _, ln := range lines {
		switch {
		case strings.Contains(ln, "from"):
			fromLine = ln
		case strings.Contains(ln, "select"):
			selectLine = ln
		case strings.Contains(ln, "where"):
			whereLine = ln
		}
	}
	if !strings.HasPrefix(fromLine, "> ") {
		t.Errorf("selected line missing '> ' marker; got %q", fromLine)
	}
	if strings.HasPrefix(selectLine, "> ") {
		t.Errorf("unselected select line has marker; got %q", selectLine)
	}
	if strings.HasPrefix(whereLine, "> ") {
		t.Errorf("unselected where line has marker; got %q", whereLine)
	}
}

func TestSuggestionsContext_HandleRender_SanitizesDisplay(t *testing.T) {
	drv := &captureDriver{}
	c := newTestSuggestions(drv)
	c.Show([]editor.Suggestion{
		{Text: "x", Display: "evil\x1b[31mred\x1b[0m"},
	}, editor.Position{})
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if strings.Contains(drv.lastContent, "\x1b") {
		t.Errorf("body contains raw ESC; sanitizer missed it: %q", drv.lastContent)
	}
}

func TestSuggestionsContext_HandleRender_WindowSlides(t *testing.T) {
	drv := &captureDriver{}
	c := newTestSuggestions(drv)
	// 10 entries, visible max 8. Selecting index 9 should slide the
	// window so index 9 is the last visible row.
	items := make([]editor.Suggestion, 10)
	for i := range items {
		items[i] = editor.Suggestion{
			Text:    string(rune('a' + i)),
			Display: string(rune('a' + i)),
		}
	}
	c.Show(items, editor.Position{})
	for range 9 {
		c.Next()
	}
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := drv.lastContent
	if strings.Contains(body, "a") {
		t.Errorf("body still contains 'a' after sliding window: %q", body)
	}
	if !strings.Contains(body, "j") {
		t.Errorf("body missing the last entry 'j': %q", body)
	}
}

func TestSuggestionsContext_HandleRender_NilDriverNoPanic(t *testing.T) {
	c := newTestSuggestions(nil)
	c.Show([]editor.Suggestion{{Text: "a", Display: "a"}}, editor.Position{})
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender with nil driver: %v", err)
	}
}

func TestFormatSuggestionsBody_ColumnRow(t *testing.T) {
	body := formatSuggestionsBody([]editor.Suggestion{
		{Text: "id", Display: "id · int4", Kind: editor.KindColumn, Detail: "int4", IsPrimaryKey: true, NotNull: true},
		{Text: "owner_id", Display: "owner_id · int4", Kind: editor.KindColumn, Detail: "int4", FKRef: "public.users.id"},
	}, 0, suggestionsVisibleMax)
	lines := strings.Split(stripSGR(body), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %d; want 2: %q", len(lines), body)
	}
	// Selected row 0: glyph '@', name "id", PK + NN tokens, type detail.
	if !strings.HasPrefix(lines[0], "> @ id") {
		t.Errorf("col row 0 = %q; want '> @ id' prefix", lines[0])
	}
	for _, tok := range []string{"int4", "PK", "NN"} {
		if !strings.Contains(lines[0], tok) {
			t.Errorf("col row 0 missing %q; got %q", tok, lines[0])
		}
	}
	// Row 1: FK arrow target.
	if !strings.Contains(lines[1], "-> public.users.id") {
		t.Errorf("col row 1 missing FK token; got %q", lines[1])
	}
	// Detail column aligns: "int4" (the type) starts at the same column
	// in both rows because the name column is padded to "owner_id".
	if strings.Index(lines[0], "int4") != strings.Index(lines[1], "int4") {
		t.Errorf("detail columns not aligned: %q vs %q", lines[0], lines[1])
	}
}

func TestSuggestionsRenderWidth_MatchesRenderedRows(t *testing.T) {
	// The width the popup layout reserves must equal the widest *rendered*
	// row (SGR stripped), or the box clips suggestions horizontally
	// (clip bug: "> ! WHER" instead of "> ! WHERE").
	cases := []struct {
		name string
		sugs []editor.Suggestion
	}{
		{
			"keyword no detail",
			[]editor.Suggestion{{Text: "WHERE", Display: "WHERE", Kind: editor.KindKeyword}},
		},
		{
			"column with detail tokens",
			[]editor.Suggestion{
				{Text: "id", Display: "id · int4", Kind: editor.KindColumn, Detail: "int4", IsPrimaryKey: true, NotNull: true},
				{Text: "owner_id", Display: "owner_id · int4", Kind: editor.KindColumn, Detail: "int4", FKRef: "public.users.id"},
			},
		},
		{
			"untyped fallback display",
			[]editor.Suggestion{{Text: "raw", Display: "raw_display_text"}},
		},
		{
			"mixed kinds",
			[]editor.Suggestion{
				{Text: "users", Display: "users", Kind: editor.KindTable},
				{Text: "select", Display: "select", Kind: editor.KindKeyword, Detail: "kw"},
				{Text: "really_long_column_name", Display: "really_long_column_name", Kind: editor.KindColumn, Detail: "timestamptz"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SuggestionsRenderWidth(tc.sugs)
			lines := strings.Split(stripSGR(formatSuggestionsBody(tc.sugs, 0, suggestionsVisibleMax)), "\n")
			want := 0
			for _, ln := range lines {
				if w := utf8.RuneCountInString(ln); w > want {
					want = w
				}
			}
			if got != want {
				t.Errorf("SuggestionsRenderWidth = %d; widest rendered row = %d\nlines: %q", got, want, lines)
			}
		})
	}
}

func TestSuggestionsRenderWidth_KeywordScreenshotCase(t *testing.T) {
	// "> " (2) + "!" (1) + " " (1) + "WHERE" (5) = 9. The old layout calc
	// returned 7 (Display+marker only), clipping the trailing "RE".
	got := SuggestionsRenderWidth([]editor.Suggestion{
		{Text: "WHERE", Display: "WHERE", Kind: editor.KindKeyword},
	})
	if got != 9 {
		t.Errorf("width = %d; want 9", got)
	}
}

func TestFormatSuggestionsBody_KindGlyphs(t *testing.T) {
	cases := []struct {
		sug   editor.Suggestion
		glyph string
		want  string // expected substring after glyph
	}{
		{editor.Suggestion{Text: "users", Display: "users", Kind: editor.KindTable}, "#", "users"},
		{editor.Suggestion{Text: "v_orders", Display: "v_orders", Kind: editor.KindView}, "%", "v_orders"},
		{editor.Suggestion{Text: "now", Display: "now", Kind: editor.KindFunction, Detail: "fn"}, "&", "fn"},
		{editor.Suggestion{Text: "select", Display: "select", Kind: editor.KindKeyword, Detail: "kw"}, "!", "kw"},
	}
	for _, tc := range cases {
		body := stripSGR(formatSuggestionsBody([]editor.Suggestion{tc.sug}, 0, suggestionsVisibleMax))
		if !strings.HasPrefix(body, "> "+tc.glyph+" ") {
			t.Errorf("kind %q: row = %q; want glyph %q", tc.sug.Kind, body, tc.glyph)
		}
		if !strings.Contains(body, tc.want) {
			t.Errorf("kind %q: row = %q; missing %q", tc.sug.Kind, body, tc.want)
		}
	}
}

func TestFormatSuggestionsBody_FallbackDisplay(t *testing.T) {
	// Empty Detail + zero typed fields => render Display exactly as the
	// pre-change behaviour (no glyph, no detail column).
	body := formatSuggestionsBody([]editor.Suggestion{
		{Text: "raw", Display: "raw_display_text"},
	}, 0, suggestionsVisibleMax)
	if body != "> raw_display_text" {
		t.Errorf("fallback row = %q; want %q", body, "> raw_display_text")
	}
	if strings.Contains(body, "\x1b") {
		t.Errorf("fallback row leaked SGR: %q", body)
	}
}

func TestFormatSuggestionsBody_EscapeStrippedSGRSurvives(t *testing.T) {
	// A crafted escape embedded in the untrusted NAME, the DB-derived
	// Detail, AND the FKRef must all be stripped, while the theme tint
	// SGR we add ourselves survives (Design D4: sanitize before compose,
	// never re-sanitize the composed row).
	body := formatSuggestionsBody([]editor.Suggestion{
		{
			Text:   "na\x1b[31mme",
			Kind:   editor.KindColumn,
			Detail: "in\x1b[32mt4",
			FKRef:  "pub\x1b[33mlic.users.id",
		},
	}, 0, suggestionsVisibleMax)

	// The crafted red/green/yellow SGR from the inputs must be gone.
	for _, evil := range []string{"\x1b[31m", "\x1b[32m", "\x1b[33m"} {
		if strings.Contains(body, evil) {
			t.Errorf("crafted escape %q survived sanitize; row = %q", evil, body)
		}
	}
	// The sanitized text (escape removed, surrounding chars kept) remains.
	for _, want := range []string{"name", "int4", "public.users.id"} {
		if !strings.Contains(body, want) {
			t.Errorf("sanitized text %q missing; row = %q", want, body)
		}
	}
	// Our theme tint SGR (cyan + reset) survives — it is added AFTER
	// sanitization and never re-sanitized.
	if sgr := theme.ColorSGR("cyan", theme.Fg); sgr != "" && !strings.Contains(body, sgr) {
		t.Errorf("theme tint SGR stripped; row = %q", body)
	}
	if !strings.Contains(body, theme.AnsiReset) {
		t.Errorf("theme reset SGR stripped; row = %q", body)
	}
}

func TestFormatSuggestionsBody_WideCJKNoPanic(t *testing.T) {
	// Wide CJK name renders without panic. Rune-count padding only;
	// exact terminal-cell alignment is a documented non-goal (D5).
	body := formatSuggestionsBody([]editor.Suggestion{
		{Text: "ascii", Display: "ascii", Kind: editor.KindColumn, Detail: "int4"},
		{Text: "列名", Display: "列名", Kind: editor.KindColumn, Detail: "text"},
	}, 0, suggestionsVisibleMax)
	if !strings.Contains(body, "列名") {
		t.Errorf("CJK name missing from body: %q", body)
	}
}

// ---- matched-name-character highlighting -------------------

func TestSuggestionHighlight_NamePrefixWrapped(t *testing.T) {
	// Matches=[0,1,2] for "order_email" => "ord" wrapped in the Search SGR,
	// the rest of the name and the detail tokens plain. The golden is built
	// from grid.HighlightRuneSpans itself so it tracks the active theme SGR.
	body := formatSuggestionsBody([]editor.Suggestion{
		{Text: "order_email", Kind: editor.KindColumn, Detail: "text", Matches: []int{0, 1, 2}},
	}, 0, suggestionsVisibleMax)

	wantName := grid.HighlightRuneSpans("order_email", [][2]int{{0, 1}, {1, 2}, {2, 3}})
	if !strings.Contains(body, wantName) {
		t.Errorf("name segment not highlighted as expected;\n got %q\nwant substring %q", body, wantName)
	}
	// Marker + glyph precede the highlighted name; detail follows, untouched.
	if !strings.HasPrefix(body, "> @ ") {
		t.Errorf("marker/glyph prefix wrong; got %q", body)
	}
	// The detail token must NOT carry the Search SGR. Strip the cyan detail
	// tint first, then confirm the Search SGR does not appear after "text".
	if got := stripSGR(body); !strings.Contains(got, "order_email") || !strings.Contains(got, "text") {
		t.Errorf("plain body missing name or detail; got %q", got)
	}
}

func TestSuggestionHighlight_DetailNeverHighlighted(t *testing.T) {
	// Whole-name match: entire name highlighted, detail tokens untouched.
	body := formatSuggestionsBody([]editor.Suggestion{
		{Text: "id", Kind: editor.KindColumn, Detail: "int4", IsPrimaryKey: true, NotNull: true, Matches: []int{0, 1}},
	}, 0, suggestionsVisibleMax)

	wantName := grid.HighlightRuneSpans("id", [][2]int{{0, 1}, {1, 2}})
	if !strings.Contains(body, wantName) {
		t.Errorf("whole-name highlight missing;\n got %q\nwant substring %q", body, wantName)
	}
	// The Search SGR prefix must appear exactly once-per-name and never wrap
	// "int4"/"PK"/"NN". Verify the detail token substring is reachable AFTER
	// closing the highlight: the detail tint is cyan, distinct from Search.
	searchSGR := grid.HighlightRuneSpans("x", [][2]int{{0, 1}})
	// searchSGR == <open>x<reset>; extract the opening prefix.
	open := searchSGR[:strings.IndexByte(searchSGR, 'x')]
	if !strings.Contains(body, open) {
		t.Fatalf("Search SGR not found in body: %q", body)
	}
	// The detail tokens ("int4 PK NN") are appended AFTER the highlighted
	// name. Locate where the detail run begins (the cyan tint SGR) and assert
	// no Search SGR appears at or after that point — highlight is name-only.
	detailTint := theme.ColorSGR(detailTokenColor, theme.Fg)
	if detailTint == "" {
		t.Skip("no detail tint SGR in active theme")
	}
	di := strings.Index(body, detailTint)
	if di < 0 {
		t.Fatalf("detail tint SGR not found: %q", body)
	}
	if strings.Contains(body[di:], open) {
		t.Errorf("Search SGR leaked into detail region; body=%q", body)
	}
}

func TestSuggestionHighlight_EmptyMatchesIdenticalToBaseline(t *testing.T) {
	sug := editor.Suggestion{Text: "order_email", Kind: editor.KindColumn, Detail: "text"}
	withNil := formatSuggestionsBody([]editor.Suggestion{sug}, 0, suggestionsVisibleMax)
	sug.Matches = []int{}
	withEmpty := formatSuggestionsBody([]editor.Suggestion{sug}, 0, suggestionsVisibleMax)
	if withNil != withEmpty {
		t.Errorf("empty Matches differs from nil Matches:\n nil   %q\n empty %q", withNil, withEmpty)
	}
	// And neither contains the Search SGR (no highlight at all).
	searchOpen := func() string {
		s := grid.HighlightRuneSpans("x", [][2]int{{0, 1}})
		return s[:strings.IndexByte(s, 'x')]
	}()
	if searchOpen != "" && strings.Contains(withNil, searchOpen) {
		t.Errorf("nil Matches leaked Search SGR: %q", withNil)
	}
}

func TestSuggestionHighlight_OutOfRangeAfterSanitizeFallback(t *testing.T) {
	// The raw name carries a mid-string escape that SanitizeCellEscapes
	// strips, shortening the name. The Matches index the RAW name, so after
	// sanitization they no longer map. Expect the whole-name fallback
	// (entire sanitized name highlighted), no panic, no mid-rune slice.
	raw := "na\x1b[31mme" // sanitizes to "name"
	body := formatSuggestionsBody([]editor.Suggestion{
		{Text: raw, Kind: editor.KindColumn, Detail: "int4", Matches: []int{0, 1, 5, 6}},
	}, 0, suggestionsVisibleMax)

	wantName := grid.HighlightRuneSpans("name", [][2]int{{0, 4}})
	if !strings.Contains(body, wantName) {
		t.Errorf("escape-shortened name did not take whole-name fallback;\n got %q\nwant substring %q", body, wantName)
	}
	// The crafted escape is gone.
	if strings.Contains(body, "\x1b[31m") {
		t.Errorf("crafted escape survived: %q", body)
	}
}

func TestSuggestionHighlight_MultibyteMidStringBoundaries(t *testing.T) {
	// Multibyte name, mid-string match on the 2nd rune. The span must land
	// on rune boundaries (no mid-rune slice / mojibake).
	name := "café_col" // 'é' is multibyte
	body := formatSuggestionsBody([]editor.Suggestion{
		{Text: name, Kind: editor.KindColumn, Detail: "text", Matches: []int{3}}, // the 'é'
	}, 0, suggestionsVisibleMax)

	wantName := grid.HighlightRuneSpans(name, [][2]int{{3, 4}})
	if !strings.Contains(body, wantName) {
		t.Errorf("multibyte mid-string highlight wrong;\n got %q\nwant substring %q", body, wantName)
	}
	// The full name still renders intact after stripping SGR.
	if got := stripSGR(body); !strings.Contains(got, name) {
		t.Errorf("multibyte name corrupted; got %q", got)
	}
}

// ---- selected-function signature help footer ---------------

// fakeDetailProvider is a stub editor.FunctionDetailProvider. cache maps
// "schema\x00name" -> details; a present key is a HIT, an absent key is a
// MISS. warmCalls records (schema,name) keys passed to WarmFunctionDetail.
// onWarm, when set, is the details a warm "lands" — the provider then
// installs it into cache and invokes the onReady callback synchronously
// (the production UI-scheduler guarantee, modelled inline for the test).
type fakeDetailProvider struct {
	cache     map[string][]models.FunctionDetail
	warmCalls []string
	onWarm    map[string][]models.FunctionDetail
	pending   []func()
}

func newFakeDetailProvider() *fakeDetailProvider {
	return &fakeDetailProvider{
		cache:  map[string][]models.FunctionDetail{},
		onWarm: map[string][]models.FunctionDetail{},
	}
}

func detailKey(schema, name string) string { return schema + "\x00" + name }

func (f *fakeDetailProvider) FunctionDetail(schema, name string) ([]models.FunctionDetail, bool) {
	d, ok := f.cache[detailKey(schema, name)]
	return d, ok
}

// WarmFunctionDetail records the warm request and, when a landing is
// staged in onWarm, captures the (deferred) ready callback in pending so
// the test can fire it AFTER the in-flight HandleRender returns — modelling
// the production async UI-loop tick (the onReady never re-enters the render
// that requested the warm).
func (f *fakeDetailProvider) WarmFunctionDetail(schema, name string, onReady func()) {
	k := detailKey(schema, name)
	f.warmCalls = append(f.warmCalls, k)
	if landed, ok := f.onWarm[k]; ok {
		f.pending = append(f.pending, func() {
			f.cache[k] = landed
			if onReady != nil {
				onReady()
			}
		})
	}
}

// flushWarms fires every deferred warm landing in order (the modelled
// UI-loop tick), then clears the queue.
func (f *fakeDetailProvider) flushWarms() {
	pending := f.pending
	f.pending = nil
	for _, fn := range pending {
		fn()
	}
}

func fnSuggestion(name string) editor.Suggestion {
	return editor.Suggestion{Text: name, Display: name + "(...)", Kind: editor.KindFunction, Detail: "fn"}
}

func TestSignatureFooter_CachedRendersLine(t *testing.T) {
	drv := &captureDriver{}
	c := newTestSuggestions(drv)
	prov := newFakeDetailProvider()
	prov.cache[detailKey("public", "lower")] = []models.FunctionDetail{
		{Name: "lower", Args: []models.FunctionArg{{Name: "str", Type: "text"}}, ReturnType: "text"},
	}
	c.SetFunctionDetailProvider(prov, func() string { return "public" })
	c.Show([]editor.Suggestion{fnSuggestion("lower")}, editor.Position{})

	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := stripSGR(drv.lastContent)
	want := "lower(str text) -> text"
	if !strings.Contains(body, want) {
		t.Errorf("footer missing %q; got %q", want, body)
	}
	if len(prov.warmCalls) != 0 {
		t.Errorf("cache HIT should not warm; warmCalls = %v", prov.warmCalls)
	}
}

func TestSignatureFooter_ZeroArgFunction(t *testing.T) {
	drv := &captureDriver{}
	c := newTestSuggestions(drv)
	prov := newFakeDetailProvider()
	prov.cache[detailKey("public", "now")] = []models.FunctionDetail{
		{Name: "now", ReturnType: "timestamptz"},
	}
	c.SetFunctionDetailProvider(prov, func() string { return "public" })
	c.Show([]editor.Suggestion{fnSuggestion("now")}, editor.Position{})

	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := stripSGR(drv.lastContent)
	if !strings.Contains(body, "now() -> timestamptz") {
		t.Errorf("zero-arg footer wrong; got %q", body)
	}
}

func TestSignatureFooter_ColdMissThenWarmRerenders(t *testing.T) {
	drv := &captureDriver{}
	c := newTestSuggestions(drv)
	prov := newFakeDetailProvider()
	// MISS initially; a warm "lands" this detail and fires onReady.
	prov.onWarm[detailKey("public", "upper")] = []models.FunctionDetail{
		{Name: "upper", Args: []models.FunctionArg{{Name: "str", Type: "text"}}, ReturnType: "text"},
	}
	c.SetFunctionDetailProvider(prov, func() string { return "public" })
	c.Show([]editor.Suggestion{fnSuggestion("upper")}, editor.Position{})

	// First render: cold miss => list ONLY (no footer) and a warm is fired.
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if len(prov.warmCalls) == 0 {
		t.Fatal("cache MISS did not invoke WarmFunctionDetail")
	}
	if strings.Contains(stripSGR(drv.lastContent), "->") {
		t.Errorf("cold render leaked a footer before warm landed; got %q", drv.lastContent)
	}
	// Warm lands on the next UI-loop tick => onReady re-renders the popup; the
	// signature now appears with NO manual user re-trigger (re-render-on-warm).
	prov.flushWarms()
	body := stripSGR(drv.lastContent)
	if !strings.Contains(body, "upper(str text) -> text") {
		t.Errorf("re-render-on-warm did not surface signature; got %q", body)
	}
}

func TestSignatureFooter_ColdMissNeverWarmsStaysEmpty(t *testing.T) {
	drv := &captureDriver{}
	c := newTestSuggestions(drv)
	prov := newFakeDetailProvider() // no cache entry, no onWarm landing
	c.SetFunctionDetailProvider(prov, func() string { return "public" })
	c.Show([]editor.Suggestion{fnSuggestion("ghost")}, editor.Position{})

	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	// Warm requested, but nothing landed: body is the list ONLY, no footer,
	// no panic, no spinner-lock.
	body := stripSGR(drv.lastContent)
	if strings.Contains(body, "->") {
		t.Errorf("permanently-cold key rendered a footer; got %q", body)
	}
	if len(prov.warmCalls) != 1 {
		t.Errorf("expected exactly one warm request; got %v", prov.warmCalls)
	}
}

func TestSignatureFooter_EscapeSanitized(t *testing.T) {
	drv := &captureDriver{}
	c := newTestSuggestions(drv)
	prov := newFakeDetailProvider()
	// Crafted escapes in the server-derived arg type AND return type.
	prov.cache[detailKey("public", "evil")] = []models.FunctionDetail{
		{
			Name:       "evil",
			Args:       []models.FunctionArg{{Name: "a", Type: "te\x1b[31mxt"}},
			ReturnType: "in\x1b[32mt4",
		},
	}
	c.SetFunctionDetailProvider(prov, func() string { return "public" })
	c.Show([]editor.Suggestion{fnSuggestion("evil")}, editor.Position{})

	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	raw := drv.lastContent
	// Crafted SGR stripped...
	for _, evil := range []string{"\x1b[31m", "\x1b[32m"} {
		if strings.Contains(raw, evil) {
			t.Errorf("crafted escape %q survived; got %q", evil, raw)
		}
	}
	// ...sanitized text retained, our theme tint survives.
	plain := stripSGR(raw)
	if !strings.Contains(plain, "text") || !strings.Contains(plain, "int4") {
		t.Errorf("sanitized segments missing; got %q", plain)
	}
	if sgr := theme.ColorSGR(signatureTokenColor, theme.Fg); sgr != "" && !strings.Contains(raw, sgr) {
		t.Errorf("theme tint SGR stripped from footer; got %q", raw)
	}
}

func TestSignatureFooter_NoColorPath(t *testing.T) {
	// formatSignatureLine must render content intact when the theme has no
	// SGR for the tint colour (no-color path). Exercise the pure formatter
	// with a colour name ColorSGR does not map.
	got := formatSignatureLineForTest(t, "f", []models.FunctionDetail{
		{Name: "f", Args: []models.FunctionArg{{Name: "x", Type: "int4"}}, ReturnType: "bool"},
	})
	if got != "f(x int4) -> bool" {
		t.Errorf("no-color footer = %q; want %q", got, "f(x int4) -> bool")
	}
}

func TestSignatureFooter_NonFunctionSelectionNoLine(t *testing.T) {
	drv := &captureDriver{}
	c := newTestSuggestions(drv)
	prov := newFakeDetailProvider()
	// A column entry sharing the name "lower" that DOES have a cached detail
	// — the footer must NOT appear because the selection is not a function.
	prov.cache[detailKey("public", "lower")] = []models.FunctionDetail{
		{Name: "lower", ReturnType: "text"},
	}
	c.SetFunctionDetailProvider(prov, func() string { return "public" })
	c.Show([]editor.Suggestion{
		{Text: "lower", Display: "lower", Kind: editor.KindColumn, Detail: "text"},
	}, editor.Position{})

	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if strings.Contains(drv.lastContent, "->") {
		t.Errorf("non-function selection rendered a footer; got %q", drv.lastContent)
	}
	if len(prov.warmCalls) != 0 {
		t.Errorf("non-function selection warmed; warmCalls = %v", prov.warmCalls)
	}
}

func TestSignatureFooter_SelectionChangeUpdatesLine(t *testing.T) {
	drv := &captureDriver{}
	c := newTestSuggestions(drv)
	prov := newFakeDetailProvider()
	prov.cache[detailKey("public", "lower")] = []models.FunctionDetail{
		{Name: "lower", Args: []models.FunctionArg{{Name: "s", Type: "text"}}, ReturnType: "text"},
	}
	prov.cache[detailKey("public", "abs")] = []models.FunctionDetail{
		{Name: "abs", Args: []models.FunctionArg{{Name: "n", Type: "int4"}}, ReturnType: "int4"},
	}
	c.SetFunctionDetailProvider(prov, func() string { return "public" })
	c.Show([]editor.Suggestion{fnSuggestion("lower"), fnSuggestion("abs")}, editor.Position{})

	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if !strings.Contains(stripSGR(drv.lastContent), "lower(s text) -> text") {
		t.Errorf("initial footer wrong; got %q", drv.lastContent)
	}
	c.Next() // select "abs"
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	plain := stripSGR(drv.lastContent)
	if !strings.Contains(plain, "abs(n int4) -> int4") {
		t.Errorf("footer did not update on selection change; got %q", plain)
	}
	if strings.Contains(plain, "lower(s text)") {
		t.Errorf("stale footer for prior selection still present; got %q", plain)
	}
}

func TestSignatureFooter_EmptyDetailNoLine(t *testing.T) {
	drv := &captureDriver{}
	c := newTestSuggestions(drv)
	prov := newFakeDetailProvider()
	// Cached HIT but EMPTY details slice (loaded-but-empty): graceful
	// absence, no footer, no warm (the key is cached).
	prov.cache[detailKey("public", "empty")] = []models.FunctionDetail{}
	c.SetFunctionDetailProvider(prov, func() string { return "public" })
	c.Show([]editor.Suggestion{fnSuggestion("empty")}, editor.Position{})

	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if strings.Contains(drv.lastContent, "->") {
		t.Errorf("empty-detail HIT rendered a footer; got %q", drv.lastContent)
	}
	if len(prov.warmCalls) != 0 {
		t.Errorf("empty-detail HIT warmed; warmCalls = %v", prov.warmCalls)
	}
}

func TestSignatureFooter_OverloadsShowCountAndFirst(t *testing.T) {
	got := formatSignatureLineForTest(t, "concat", []models.FunctionDetail{
		{Name: "concat", Args: []models.FunctionArg{{Name: "a", Type: "text"}}, ReturnType: "text"},
		{Name: "concat", Args: []models.FunctionArg{{Name: "a", Type: "int4"}}, ReturnType: "int4"},
		{Name: "concat", Args: []models.FunctionArg{{Name: "a", Type: "bool"}}, ReturnType: "bool"},
	})
	// First overload rendered + "(+2 overloads)" count.
	if !strings.Contains(got, "concat(a text) -> text") {
		t.Errorf("first overload not rendered; got %q", got)
	}
	if !strings.Contains(got, "(+2 overloads)") {
		t.Errorf("overload count missing/wrong; got %q", got)
	}
}

func TestSignatureFooter_VariadicAndOutModePrefix(t *testing.T) {
	got := formatSignatureLineForTest(t, "f", []models.FunctionDetail{
		{
			Name: "f",
			Args: []models.FunctionArg{
				{Name: "a", Type: "int4", Mode: "IN"},
				{Name: "rest", Type: "text", Mode: "VARIADIC"},
				{Name: "out1", Type: "int4", Mode: "OUT"},
			},
			ReturnType: "record",
		},
	})
	// IN gets no prefix; VARIADIC and OUT do.
	if !strings.Contains(got, "a int4") {
		t.Errorf("IN arg should have no mode prefix; got %q", got)
	}
	if !strings.Contains(got, "VARIADIC rest text") {
		t.Errorf("VARIADIC prefix missing; got %q", got)
	}
	if !strings.Contains(got, "OUT out1 int4") {
		t.Errorf("OUT prefix missing; got %q", got)
	}
}

func TestSignatureFooter_NilProviderNoLine(t *testing.T) {
	drv := &captureDriver{}
	c := newTestSuggestions(drv)
	// No provider wired.
	c.Show([]editor.Suggestion{fnSuggestion("lower")}, editor.Position{})
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if strings.Contains(drv.lastContent, "->") {
		t.Errorf("nil provider rendered a footer; got %q", drv.lastContent)
	}
}

// formatSignatureLineForTest strips any theme tint SGR so the assertion
// matches plain content regardless of the active theme (no-color and
// colour paths both reduce to the same plain signature string).
func formatSignatureLineForTest(t *testing.T, name string, details []models.FunctionDetail) string {
	t.Helper()
	return stripSGR(formatSignatureLine(name, details))
}

// ---- per-kind exact-render goldens ------------------------

// TestFormatSuggestionsBody_PerKindGoldens pins the EXACT rendered row
// (marker + glyph + name + aligned detail tokens) for each Kind. The detail
// tint SGR is stripped so the golden tracks the plain content contract; the
// presence of the tint SGR is covered separately by the escape test.
func TestFormatSuggestionsBody_PerKindGoldens(t *testing.T) {
	cases := []struct {
		name string
		sug  editor.Suggestion
		want string
	}{
		{
			name: "column type+PK+FK+NN tokens",
			sug:  editor.Suggestion{Text: "owner_id", Kind: editor.KindColumn, Detail: "int4", IsPrimaryKey: true, FKRef: "public.users.id", NotNull: true},
			want: "> @ owner_id  int4 PK -> public.users.id NN",
		},
		{
			name: "table bare detail",
			sug:  editor.Suggestion{Text: "users", Kind: editor.KindTable, Detail: "table"},
			want: "> # users  table",
		},
		{
			name: "view bare detail",
			sug:  editor.Suggestion{Text: "v_orders", Kind: editor.KindView, Detail: "view"},
			want: "> % v_orders  view",
		},
		{
			name: "function bare detail",
			sug:  editor.Suggestion{Text: "now", Kind: editor.KindFunction, Detail: "fn"},
			want: "> & now  fn",
		},
		{
			name: "keyword bare detail",
			sug:  editor.Suggestion{Text: "select", Kind: editor.KindKeyword, Detail: "keyword"},
			want: "> ! select  keyword",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := stripSGR(formatSuggestionsBody([]editor.Suggestion{tc.sug}, 0, suggestionsVisibleMax))
			if body != tc.want {
				t.Errorf("row golden mismatch;\n got %q\nwant %q", body, tc.want)
			}
		})
	}
}

// ---- no-color (NO_COLOR / monochrome) ---------------------

// TestFormatSuggestionsBody_NoColorEmitsNoSGR drives IsMonochrome=true and
// asserts a typed, matched row renders WITHOUT any SGR (no detail tint, no
// match-highlight) and without panic, while the plain content (glyph, name,
// detail tokens) stays intact. Restores the cache afterward.
func TestFormatSuggestionsBody_NoColorEmitsNoSGR(t *testing.T) {
	restore := theme.SetMonochromeForTest(true)
	defer restore()

	body := formatSuggestionsBody([]editor.Suggestion{
		{Text: "id", Kind: editor.KindColumn, Detail: "int4", IsPrimaryKey: true, NotNull: true, Matches: []int{0, 1}},
		{Text: "owner_id", Kind: editor.KindColumn, Detail: "int8", FKRef: "public.users.id", Matches: []int{0}},
	}, 0, suggestionsVisibleMax)

	if strings.Contains(body, "\x1b") {
		t.Fatalf("no-color body leaked an SGR escape: %q", body)
	}
	// Plain content survives.
	for _, want := range []string{"@ id", "int4", "PK", "NN", "owner_id", "-> public.users.id"} {
		if !strings.Contains(body, want) {
			t.Errorf("no-color body missing %q; got %q", want, body)
		}
	}
}

// TestSignatureFooter_NoColorRenderEmitsNoSGR drives IsMonochrome=true through
// the full HandleRender path (list + signature footer) and asserts the emitted
// content carries no SGR, no panic, with the signature intact.
func TestSignatureFooter_NoColorRenderEmitsNoSGR(t *testing.T) {
	restore := theme.SetMonochromeForTest(true)
	defer restore()

	drv := &captureDriver{}
	c := newTestSuggestions(drv)
	prov := newFakeDetailProvider()
	prov.cache[detailKey("public", "lower")] = []models.FunctionDetail{
		{Name: "lower", Args: []models.FunctionArg{{Name: "str", Type: "text"}}, ReturnType: "text"},
	}
	c.SetFunctionDetailProvider(prov, func() string { return "public" })
	c.Show([]editor.Suggestion{fnSuggestion("lower")}, editor.Position{})

	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if strings.Contains(drv.lastContent, "\x1b") {
		t.Fatalf("no-color render leaked an SGR escape: %q", drv.lastContent)
	}
	if !strings.Contains(drv.lastContent, "lower(str text) -> text") {
		t.Errorf("no-color footer content missing; got %q", drv.lastContent)
	}
}

// TestHighlightName_NoColorSuppressesHighlight pins the unit contract: under
// monochrome, highlightName returns the plain sanitized name (matched runes
// present, no highlight SGR), identical to the no-matches baseline.
func TestHighlightName_NoColorSuppressesHighlight(t *testing.T) {
	restore := theme.SetMonochromeForTest(true)
	defer restore()

	got := highlightName("order", "order", []int{0, 1, 2})
	if got != "order" {
		t.Errorf("no-color highlightName = %q; want plain %q", got, "order")
	}
}

// ---- window edges -----------------------------------------

func TestFormatSuggestionsBody_EmptyWindow(t *testing.T) {
	if body := formatSuggestionsBody(nil, 0, suggestionsVisibleMax); body != "" {
		t.Errorf("empty suggestions body = %q; want \"\"", body)
	}
}

func TestFormatSuggestionsBody_SingleRow(t *testing.T) {
	body := formatSuggestionsBody([]editor.Suggestion{
		{Text: "only", Kind: editor.KindKeyword, Detail: "kw"},
	}, 0, suggestionsVisibleMax)
	if strings.Contains(body, "\n") {
		t.Errorf("single-row body contains newline: %q", body)
	}
	if !strings.HasPrefix(stripSGR(body), "> ! only") {
		t.Errorf("single-row golden wrong; got %q", body)
	}
}

func TestFormatSuggestionsBody_FullEightRowWindow(t *testing.T) {
	items := make([]editor.Suggestion, suggestionsVisibleMax)
	for i := range items {
		items[i] = editor.Suggestion{Text: string(rune('a' + i)), Kind: editor.KindKeyword, Detail: "kw"}
	}
	body := formatSuggestionsBody(items, 0, suggestionsVisibleMax)
	lines := strings.Split(body, "\n")
	if len(lines) != suggestionsVisibleMax {
		t.Fatalf("full window lines = %d; want %d", len(lines), suggestionsVisibleMax)
	}
	// Exactly one selected marker (row 0); the rest are unselected.
	if !strings.HasPrefix(lines[0], "> ") {
		t.Errorf("row 0 missing selected marker; got %q", lines[0])
	}
	for i := 1; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i], "  ") || strings.HasPrefix(lines[i], "> ") {
			t.Errorf("row %d should be unselected; got %q", i, lines[i])
		}
	}
}

func TestSuggestionHighlight_MarkerDoesNotShiftRunes(t *testing.T) {
	// Two rows: row 0 selected ("> " marker), row 1 not ("  " marker). The
	// SAME Matches must highlight the SAME name runes regardless of the 2-col
	// marker width. Golden includes the marker.
	sug := editor.Suggestion{Text: "order_email", Kind: editor.KindColumn, Detail: "text", Matches: []int{0, 1, 2}}
	body := formatSuggestionsBody([]editor.Suggestion{sug, sug}, 0, suggestionsVisibleMax)
	lines := strings.Split(body, "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines; got %d: %q", len(lines), body)
	}
	wantName := grid.HighlightRuneSpans("order_email", [][2]int{{0, 1}, {1, 2}, {2, 3}})
	wantSelected := "> @ " + wantName
	wantUnselected := "  @ " + wantName
	if !strings.HasPrefix(lines[0], wantSelected) {
		t.Errorf("selected row golden mismatch;\n got %q\nwant prefix %q", lines[0], wantSelected)
	}
	if !strings.HasPrefix(lines[1], wantUnselected) {
		t.Errorf("unselected row golden mismatch;\n got %q\nwant prefix %q", lines[1], wantUnselected)
	}
}
