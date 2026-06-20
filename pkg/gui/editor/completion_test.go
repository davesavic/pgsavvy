package editor

import (
	"context"
	"sync"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/models"
)

// stubSource is a hand-rolled Source for engine tests. Returns its
// stored items verbatim; honours Name/Priority for tiebreak coverage.
type stubSource struct {
	name     string
	priority int
	items    []Suggestion
}

func (s *stubSource) Name() string  { return s.name }
func (s *stubSource) Priority() int { return s.priority }
func (s *stubSource) Suggest(_ context.Context, _ *Buffer, _ Position) []Suggestion {
	// Tag Source field if not already populated so dedupe + audit
	// can rely on it.
	out := make([]Suggestion, len(s.items))
	for i, it := range s.items {
		if it.Source == "" {
			it.Source = s.name
		}
		out[i] = it
	}
	return out
}

func TestEngine_NoSources_ReturnsEmpty(t *testing.T) {
	e := NewEngine(nil)
	got := e.Trigger(context.Background(), nil, Position{})
	if got == nil {
		t.Fatal("Trigger returned nil; want empty non-nil slice")
	}
	if len(got) != 0 {
		t.Fatalf("len = %d; want 0", len(got))
	}
}

func TestEngine_AllEmptySources_ReturnsEmpty(t *testing.T) {
	a := &stubSource{name: "a", priority: 1}
	b := &stubSource{name: "b", priority: 1}
	e := NewEngine([]Source{a, b})
	got := e.Trigger(context.Background(), nil, Position{})
	if got == nil {
		t.Fatal("Trigger returned nil; want empty non-nil slice")
	}
	if len(got) != 0 {
		t.Fatalf("len = %d; want 0", len(got))
	}
}

func TestEngine_DedupeByText_KeepsHigherScore(t *testing.T) {
	a := &stubSource{name: "a", priority: 1, items: []Suggestion{
		{Text: "select", Display: "select (lo)", Score: 1},
	}}
	b := &stubSource{name: "b", priority: 1, items: []Suggestion{
		{Text: "select", Display: "select (hi)", Score: 10},
	}}
	e := NewEngine([]Source{a, b})
	got := e.Trigger(context.Background(), nil, Position{})
	if len(got) != 1 {
		t.Fatalf("len = %d; want 1 (deduped)", len(got))
	}
	if got[0].Score != 10 {
		t.Errorf("kept Score = %d; want 10 (higher wins)", got[0].Score)
	}
	if got[0].Display != "select (hi)" {
		t.Errorf("kept Display = %q; want %q", got[0].Display, "select (hi)")
	}
}

func TestEngine_DedupeByText_TieScoreKeepsHigherPriority(t *testing.T) {
	low := &stubSource{name: "low", priority: 1, items: []Suggestion{
		{Text: "from", Display: "from (low)", Score: 5},
	}}
	high := &stubSource{name: "high", priority: 10, items: []Suggestion{
		{Text: "from", Display: "from (high)", Score: 5},
	}}
	e := NewEngine([]Source{low, high})
	got := e.Trigger(context.Background(), nil, Position{})
	if len(got) != 1 {
		t.Fatalf("len = %d; want 1", len(got))
	}
	if got[0].Source != "high" {
		t.Errorf("kept Source = %q; want %q (higher Priority wins tie)", got[0].Source, "high")
	}
}

func TestEngine_Sort_ScoreDescThenPriorityDesc(t *testing.T) {
	a := &stubSource{name: "a", priority: 1, items: []Suggestion{
		{Text: "one", Score: 1},
		{Text: "two", Score: 5},
	}}
	b := &stubSource{name: "b", priority: 10, items: []Suggestion{
		{Text: "three", Score: 5}, // same score as "two" but higher priority
		{Text: "four", Score: 9},
	}}
	e := NewEngine([]Source{a, b})
	got := e.Trigger(context.Background(), nil, Position{})
	if len(got) != 4 {
		t.Fatalf("len = %d; want 4", len(got))
	}
	wantOrder := []string{"four", "three", "two", "one"}
	for i, want := range wantOrder {
		if got[i].Text != want {
			t.Errorf("got[%d].Text = %q; want %q (order=%v)", i, got[i].Text, want, suggestionTexts(got))
		}
	}
}

func TestEngine_AddSource_NilNoop(t *testing.T) {
	e := NewEngine(nil)
	e.AddSource(nil)
	if got := e.Sources(); got != nil {
		t.Errorf("Sources() = %v; want nil after AddSource(nil)", got)
	}
}

func TestEngine_AddSource_AppendsAndUsed(t *testing.T) {
	e := NewEngine(nil)
	s := &stubSource{name: "x", priority: 1, items: []Suggestion{{Text: "hi", Score: 1}}}
	e.AddSource(s)
	got := e.Trigger(context.Background(), nil, Position{})
	if len(got) != 1 || got[0].Text != "hi" {
		t.Fatalf("Trigger after AddSource = %+v; want one suggestion 'hi'", got)
	}
}

// TestEngine_NilMatches_SortsIdentically pins that a Suggestion with a
// nil Matches field (the zero-default for sources that have not adopted
// the matcher) sorts and dedupes exactly as before — no panic, same
// order (edge path).
func TestEngine_NilMatches_SortsIdentically(t *testing.T) {
	a := &stubSource{name: "a", priority: 1, items: []Suggestion{
		{Text: "one", Score: 1}, // Matches nil
		{Text: "two", Score: 5},
	}}
	b := &stubSource{name: "b", priority: 10, items: []Suggestion{
		{Text: "three", Score: 5},
		{Text: "four", Score: 9},
	}}
	e := NewEngine([]Source{a, b})
	got := e.Trigger(context.Background(), nil, Position{})
	wantOrder := []string{"four", "three", "two", "one"}
	if len(got) != len(wantOrder) {
		t.Fatalf("len = %d; want %d", len(got), len(wantOrder))
	}
	for i, want := range wantOrder {
		if got[i].Text != want {
			t.Errorf("got[%d].Text = %q; want %q", i, got[i].Text, want)
		}
		if got[i].Matches != nil {
			t.Errorf("got[%d].Matches = %v; want nil (unchanged)", i, got[i].Matches)
		}
	}
}

// TestEngine_Trigger_CapsResults pins the top-N cap (Finding C): a
// candidate set larger than maxTriggerResults is truncated to exactly
// that many after dedupe, while a set at or below the cap is returned
// whole.
func TestEngine_Trigger_CapsResults(t *testing.T) {
	items := make([]Suggestion, maxTriggerResults+50)
	for i := range items {
		// Unique Text so dedupe keeps every one; descending Score so
		// the order is deterministic and the cap truncates the tail.
		items[i] = Suggestion{
			Text:  "kw" + itoa(i),
			Score: len(items) - i,
		}
	}
	e := NewEngine([]Source{&stubSource{name: "s", priority: 1, items: items}})
	got := e.Trigger(context.Background(), nil, Position{})
	if len(got) != maxTriggerResults {
		t.Fatalf("len = %d; want cap %d", len(got), maxTriggerResults)
	}
	// Tail truncated: the lowest-scored ("kw<last>") must be gone.
	if got[len(got)-1].Text == "kw"+itoa(len(items)-1) {
		t.Errorf("lowest-scored suggestion survived the cap")
	}
}

// itoa is a tiny base-10 int formatter to avoid importing strconv just
// for the cap test.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func TestSanitizeText_StripsControlsAndNewlines(t *testing.T) {
	in := "select\n\tfrom\rwh\x00ere\x1b[31m;\x7f"
	want := "selectfromwhere[31m;" // ESC stripped (it's a C0 control), but '[31m;' chars remain
	got := SanitizeText(in)
	if got != want {
		t.Errorf("SanitizeText(%q) = %q; want %q", in, got, want)
	}
}

func TestSanitizeText_Empty(t *testing.T) {
	if got := SanitizeText(""); got != "" {
		t.Errorf("SanitizeText(\"\") = %q; want empty", got)
	}
}

func suggestionTexts(s []Suggestion) []string {
	out := make([]string, len(s))
	for i, it := range s {
		out[i] = it.Text
	}
	return out
}

// TestAutoTriggerFromContext_Cases pins the C5 auto-trigger detection
// behaviour. Reuses bufWithCursor (defined in completion_schema_source_test.go).
func TestAutoTriggerFromContext_Cases(t *testing.T) {
	cases := []struct {
		name string
		line string
		want bool
	}{
		// Positive — table-context keywords with trailing space.
		{"trailing FROM space", "SELECT * FROM ", true},
		{"trailing JOIN space", "SELECT * FROM users JOIN ", true},
		{"trailing INNER JOIN", "SELECT * FROM a INNER JOIN ", true},
		{"trailing LEFT JOIN", "SELECT * FROM a LEFT JOIN ", true},
		{"trailing RIGHT JOIN", "SELECT * FROM a RIGHT JOIN ", true},
		{"trailing CROSS JOIN", "SELECT * FROM a CROSS JOIN ", true},
		{"trailing UPDATE", "UPDATE ", true},
		{"trailing INTO", "INSERT INTO ", true},
		// Positive — identifier dot.
		{"users dot", "SELECT users.", true},
		{"qualified dot mid-expr", "WHERE u.id = users.", true},
		// Case-insensitivity sanity.
		{"lowercase from", "select * from ", true},
		// Positive — unqualified column contexts.
		{"after WHERE", "SELECT * FROM t WHERE ", true},
		{"after comparison operator", "SELECT * FROM t WHERE id = ", true},
		{"after AND", "SELECT * FROM t WHERE id = 1 AND ", true},
		{"column partial after SELECT", "SELECT user", true},
		// Positive — broadened >=2-rune identifier-prefix gate:
		// a partial identifier of two or more runes
		// auto-opens even with no governing clause keyword/operator/dot.
		{"bare 2-rune prefix", "us", true},
		{"bare 2-rune prefix mid-expr", "SELECT a, us", true},
		{"underscore-digit prefix", "u_1", true},
		// Quoted-identifier prefix: identifierPrefixAt's rune scan stops at
		// the double-quote, so the prefix is `Co` (2 runes) -> opens. The
		// leading `"` is not part of the identifier run.
		{"quoted ident prefix Co", `SELECT "Co`, true},
		// Negative — a single-rune prefix is below the threshold.
		{"bare 1-rune prefix", "u", false},
		// Engine-backed. `SELECT "C` and `SELECT a, ` now sit in a
		// SELECT column context (ExpectColumns) per the clause model, so the
		// NARROW gate (IsSchemaCompletableContext) fires regardless of prefix
		// width. The old regex required the partial to ABUT the SELECT
		// keyword, so these were false. Meaning changed — see Deviations.
		{"single-quote-stop 1-rune now column ctx", `SELECT "C`, true},
		{"trailing comma now column ctx", "SELECT a, ", true},
		// Negative — alias position: after a complete table name in a
		// FROM/JOIN clause the cursor names an alias, where table AND
		// broadened keyword/snippet suggestions are noise. Suppressed at
		// every prefix width, including the >=2-rune one the broadened gate
		// would otherwise open on.
		{"alias slot empty prefix", "SELECT * FROM users ", false},
		{"alias slot 1-rune prefix", "SELECT * FROM users a", false},
		{"alias slot 2-rune prefix overrides broadened gate", "SELECT * FROM users al", false},
		{"comma re-opens table list (not alias)", "SELECT * FROM users, ", true},
		// Negative — inside string literal (stripNoise drops it).
		{"FROM inside string", "SELECT 'FROM ", false},
		{"column ctx inside string", "SELECT * FROM t WHERE 'x", false},
		// Negative — a 2-rune prefix INSIDE an open string literal must not
		// trigger: the `open` early-return wins before the prefix gate.
		{"2-rune prefix inside string", "SELECT 'us", false},
		// Negative — inside line comment.
		{"FROM inside line comment", "SELECT 1 -- FROM ", false},
		// Negative — a 2-rune prefix inside a line comment must not trigger.
		{"2-rune prefix inside comment", "SELECT 1 -- us", false},
		// Negative — empty / nil-equivalent line.
		{"empty line", "", false},
		// Negative — cursor at column 0 (empty buffer) does not panic/trigger;
		// covered by the empty-line case above (cursor sits at col 0).
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, p := bufWithCursor(tc.line)
			got := AutoTriggerFromContext(b, p)
			if got != tc.want {
				t.Errorf("AutoTriggerFromContext(%q) = %v; want %v", tc.line, got, tc.want)
			}
		})
	}
}

// TestInAliasSlot pins the alias-position predicate the controller uses to
// dismiss the popup: true once a complete table name has been consumed in a
// FROM/JOIN clause (the cursor names an alias), false while the table name is
// still being typed, in a fresh slot, after a comma, or outside a table
// clause. Reuses bufWithCursor (defined in completion_schema_source_test.go).
func TestInAliasSlot(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"select * from users", false},     // still typing the table name
		{"select * from users ", true},     // alias slot (space after table)
		{"select * from users a", true},    // alias being typed
		{"select * from ", false},          // fresh table slot
		{"select * from users, ", false},   // comma re-opens the table list
		{"select id from t where ", false}, // column clause, not a table slot
		{"", false},                        // empty buffer
	}
	for _, tc := range cases {
		b, p := bufWithCursor(tc.line)
		if got := InAliasSlot(b, p); got != tc.want {
			t.Errorf("InAliasSlot(%q) = %v; want %v", tc.line, got, tc.want)
		}
	}
}

// TestAutoTriggerFromContext_NilBuffer guards against panics when the
// editor wires a nil buffer (defensive: VimEditor itself nil-checks
// before invoking the callback, but the helper must be safe in
// isolation).
func TestAutoTriggerFromContext_NilBuffer(t *testing.T) {
	if AutoTriggerFromContext(nil, Position{}) {
		t.Fatal("AutoTriggerFromContext(nil, _) = true; want false")
	}
}

// TestIsSchemaCompletableContext_SuggestConsistency pins the reinterpreted
// consistency rule: the NARROW gate IsSchemaCompletableContext is
// true iff SchemaSource.Suggest returns a non-empty schema list, for every
// tested cursor position. (The pre-6.1 rule named AutoTriggerFromContext;
// post-6.1 that gate also fires on bare >=2-rune prefixes where Suggest is
// empty, so the consistency invariant moves to the narrow gate — see the
// task's Deviation note.)
//
// The session is populated (users/orders + columns) so every structured
// position that resolves a table yields a non-empty Suggest, matching the
// gate. Positions with no resolvable table (a column context with no FROM)
// are intentionally excluded — that empty-Suggest-but-true-gate case
// predates 1.3 (the regex gate had the same property) and is governed by
// TestSchemaSource_UnqualifiedColumns_NoTableInScope_Empty.
func TestIsSchemaCompletableContext_SuggestConsistency(t *testing.T) {
	lines := []string{
		// Structured + resolvable -> gate true, Suggest non-empty.
		"SELECT * FROM ",
		"SELECT * FROM users JOIN ",
		"SELECT users.",
		"SELECT * FROM users WHERE ",
		"SELECT * FROM users WHERE id = ",
		"SELECT * FROM users u WHERE u.",
		"SELECT * FROM users JOIN orders ON ",
		"SELECT name FROM users WHERE na",
		`SELECT "MyTable".`,
		// Non-structured -> gate false, Suggest empty.
		"",              // empty
		"SELECT 'open",  // inside a string literal (noise)
		"SELECT 1 -- x", // inside a line comment (noise)
		// NOTE: column contexts with NO resolvable FROM table (`SELECT `,
		// `SELECT a, `) are deliberately EXCLUDED — there the gate is true but
		// Suggest is empty (no table to draw columns from). That asymmetry
		// predates the engine gate (the regex gate had it too) and is governed by
		// TestSchemaSource_UnqualifiedColumns_NoTableInScope_Empty.
	}
	mkSrc := func() *SchemaSource {
		m := newFakeMeta()
		m.setTables("public", "users", "orders", "MyTable")
		cols := []models.Column{{Name: "id"}, {Name: "name"}}
		m.setColumns("public", "users", cols...)
		m.setColumns("public", "orders", cols...)
		m.setColumns("public", "MyTable", cols...)
		return NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	}
	for _, line := range lines {
		t.Run(line, func(t *testing.T) {
			b, p := bufWithCursor(line)
			gate := IsSchemaCompletableContext(b, p)
			suggestNonEmpty := len(mkSrc().Suggest(context.Background(), b, p)) > 0
			if gate != suggestNonEmpty {
				t.Errorf("line %q: IsSchemaCompletableContext=%v but Suggest non-empty=%v; want equal",
					line, gate, suggestNonEmpty)
			}
		})
	}
}

// TestIsSchemaCompletableContext_QuotedAndSchemaQualified verifies the
// engine-backed gate now recognises quoted and schema-qualified dot
// contexts the old regex missed.
func TestIsSchemaCompletableContext_QuotedAndSchemaQualified(t *testing.T) {
	for _, line := range []string{
		`SELECT "MyTable".`,
		"SELECT public.users.",
		"SELECT users u FROM users WHERE u.id", // dot-with-partial resolves
	} {
		b, p := bufWithCursor(line)
		if !IsSchemaCompletableContext(b, p) {
			t.Errorf("IsSchemaCompletableContext(%q) = false; want true (engine-backed)", line)
		}
	}
}

// TestIsIdentDotContext_EngineBacked covers the dot/qualifier trigger via
// the engine, including the dot-with-partial back-off (users.cr).
func TestIsIdentDotContext_EngineBacked(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"SELECT users.", true},
		{"SELECT users.cr", true},
		{`SELECT "MyTable".`, true},
		{"SELECT users", false},
		{"SELECT * FROM ", false},
		{"SELECT users .", false}, // space before dot -> no qualifier
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.line, func(t *testing.T) {
			b, p := bufWithCursor(tc.line)
			if got := IsIdentDotContext(b, p); got != tc.want {
				t.Errorf("IsIdentDotContext(%q) = %v; want %v", tc.line, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// cross-source composite-ranking integration tests.
//
// These tests wire the REAL sources (SchemaSource/FunctionSource/KeywordsSource/
// HistorySource) — each running editor.Match for genuine fuzzy scoring — through
// Engine.Trigger and assert OUTPUT ORDER. They prove the epic's source-weighted
// composite contract end-to-end: Score = matchQuality + sourceBias, ranking
// Schema(80) > Function(60) > Keyword(40) > History(20).
//
// A structural constraint of the design shapes how the four-way ordering is
// asserted: SchemaSource only emits in a STRUCTURED schema-completable context
// (FROM/JOIN/dot/column), while HistorySource deliberately stays SILENT in
// exactly those contexts (IsSchemaCompletableContext gate, completion_history_
// source.go) — whole-statement history is noise where tables/columns should
// lead. So schema and history are mutually exclusive at any single cursor
// position; no one position can exhibit all four with non-trivial schema AND
// history contributions. The full chain is therefore proven across two
// overlapping comparable-quality contexts (schema>function>keyword at a FROM
// context; function>keyword>history at a bare-prefix context), plus a single
// Engine wiring all four sources together (TestEngine_AllFourSources_Wired).
// ---------------------------------------------------------------------------

// fakeHistoryRows is a trivial HistoryStore returning canned rows. Distinct
// from completion_history_source_test.go's fakeHistoryStore so the two test
// files stay independent; both satisfy the HistoryStore interface.
type fakeHistoryRows struct {
	mu   sync.Mutex
	rows []string
}

func (f *fakeHistoryRows) SearchByPrefix(_ context.Context, _ string, _ int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.rows))
	copy(out, f.rows)
	return out, nil
}

// firstIndexBySource returns the index of the first suggestion produced by the
// named source, or -1 if none.
func firstIndexBySource(got []Suggestion, source string) int {
	for i, s := range got {
		if s.Source == source {
			return i
		}
	}
	return -1
}

// TestEngine_ComparableQuality_SchemaFunctionKeyword proves that, at a FROM
// context where the prefix "se" matches a table, a function, AND keywords all at
// IDENTICAL match quality (q=78), the composite Score orders the sources
// schema > function > keyword via the 80>60>40 bias chain. History is silent
// here by design (schema-completable context), so the keyword>history half is
// covered by TestEngine_ComparableQuality_FunctionKeywordHistory.
func TestEngine_ComparableQuality_SchemaFunctionKeyword(t *testing.T) {
	m := newFakeMeta()
	m.setTables("public", "session_data")
	m.setFunctions("set_config")
	schema := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	fn := NewFunctionSource(m)
	store := &fakeHistoryRows{rows: []string{"SET search_path TO public"}}
	eng := NewEngine([]Source{
		schema,
		fn,
		KeywordsSource{PriorityVal: 20},
		HistorySource{Store: store, PriorityVal: 5},
	})

	b, p := bufWithCursor("SELECT * FROM se")
	got := eng.Trigger(context.Background(), b, p)
	if len(got) == 0 {
		t.Fatal("Trigger returned no suggestions")
	}

	// Precondition: equal match quality across the three contributing sources.
	_, qT, _ := Match("se", "session_data")
	_, qF, _ := Match("se", "set_config")
	_, qK, _ := Match("se", "SELECT")
	if qT != qF || qF != qK {
		t.Fatalf("precondition: qualities differ (table %d, fn %d, kw %d); must be equal", qT, qF, qK)
	}

	iSchema := firstIndexBySource(got, SchemaSourceName)
	iFn := firstIndexBySource(got, FunctionSourceName)
	iKw := firstIndexBySource(got, KeywordsSourceName)
	iHist := firstIndexBySource(got, HistorySourceName)
	if iSchema < 0 || iFn < 0 || iKw < 0 {
		t.Fatalf("missing a source: schema=%d fn=%d kw=%d (got %v)", iSchema, iFn, iKw, suggestionTexts(got))
	}
	if iSchema >= iFn || iFn >= iKw {
		t.Fatalf("ordering schema<fn<kw violated: schema@%d fn@%d kw@%d (got %v)",
			iSchema, iFn, iKw, suggestionTexts(got))
	}
	if iHist != -1 {
		t.Fatalf("history contributed at a schema-completable context (idx %d); must be silent there", iHist)
	}
	if iSchema != 0 {
		t.Errorf("schema not at got[0] (idx %d); want top of comparable-quality competition", iSchema)
	}
}

// TestEngine_ComparableQuality_FunctionKeywordHistory completes the rank chain:
// at a BARE-prefix context (no FROM/dot), schema is silent but history fires, so
// the prefix "se" puts function > keyword > history at equal match quality via
// the 60>40>20 bias chain.
func TestEngine_ComparableQuality_FunctionKeywordHistory(t *testing.T) {
	m := newFakeMeta()
	m.setFunctions("set_config")
	fn := NewFunctionSource(m)
	store := &fakeHistoryRows{rows: []string{"SET search_path TO public"}}
	eng := NewEngine([]Source{
		fn,
		KeywordsSource{PriorityVal: 20},
		HistorySource{Store: store, PriorityVal: 5},
	})

	b, p := bufWithCursor("se")
	got := eng.Trigger(context.Background(), b, p)
	if len(got) == 0 {
		t.Fatal("Trigger returned no suggestions")
	}

	_, qF, _ := Match("se", "set_config")
	_, qK, _ := Match("se", "SELECT")
	_, qH, _ := Match("se", "SET search_path TO public")
	if qF != qK || qK != qH {
		t.Fatalf("precondition: qualities differ (fn %d, kw %d, hist %d); must be equal", qF, qK, qH)
	}

	iFn := firstIndexBySource(got, FunctionSourceName)
	iKw := firstIndexBySource(got, KeywordsSourceName)
	iHist := firstIndexBySource(got, HistorySourceName)
	if iFn < 0 || iKw < 0 || iHist < 0 {
		t.Fatalf("missing a source: fn=%d kw=%d hist=%d (got %v)", iFn, iKw, iHist, suggestionTexts(got))
	}
	if iFn >= iKw || iKw >= iHist {
		t.Fatalf("ordering fn<kw<hist violated: fn@%d kw@%d hist@%d (got %v)",
			iFn, iKw, iHist, suggestionTexts(got))
	}
}

// TestEngine_AllFourSources_Wired satisfies the literal AC: a single Engine
// wires schema+function+keyword+history and Trigger produces a ranked,
// non-empty result. At a FROM context the schema/function/keyword chain holds
// and history is (correctly) suppressed — proving the four sources coexist in
// one Engine without interference.
func TestEngine_AllFourSources_Wired(t *testing.T) {
	m := newFakeMeta()
	m.setTables("public", "session_data")
	m.setFunctions("set_config")
	schema := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	fn := NewFunctionSource(m)
	store := &fakeHistoryRows{rows: []string{"SELECT 1"}}
	eng := NewEngine([]Source{
		schema,
		fn,
		KeywordsSource{PriorityVal: 20},
		HistorySource{Store: store, PriorityVal: 5},
	})
	if len(eng.Sources()) != 4 {
		t.Fatalf("Engine wired %d sources; want 4", len(eng.Sources()))
	}

	b, p := bufWithCursor("SELECT * FROM se")
	got := eng.Trigger(context.Background(), b, p)
	if len(got) == 0 {
		t.Fatal("four-source Engine returned an empty popup")
	}
	if got[0].Source != SchemaSourceName {
		t.Errorf("got[0].Source = %q; want schema to lead the four-source ranking", got[0].Source)
	}
}

// TestEngine_SuperiorLowerPriorityBeatsWeakHigherPriority proves composite Score
// — not pure source priority — drives ranking: a CLEARLY SUPERIOR keyword match
// (low bias 40) outranks a WEAK schema match (high bias 80) when the match-
// quality gap exceeds the bias gap. Prefix "select" exactly matches keyword
// SELECT (q=206 -> composite 246) but only scatter-matches the schema table
// "sample_collection_target" (q=150 -> composite 230); the keyword wins despite
// the lower source bias, and the schema candidate still survives (ranked lower).
func TestEngine_SuperiorLowerPriorityBeatsWeakHigherPriority(t *testing.T) {
	m := newFakeMeta()
	m.setTables("public", "sample_collection_target")
	schema := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	eng := NewEngine([]Source{schema, KeywordsSource{PriorityVal: 20}})

	b, p := bufWithCursor("SELECT * FROM select")
	got := eng.Trigger(context.Background(), b, p)
	if len(got) == 0 {
		t.Fatal("Trigger returned no suggestions")
	}

	// Precondition: the keyword's match quality must clearly exceed the schema's
	// so the composite (kw + 40) beats (schema + 80) — a quality win, not bias.
	_, qKw, _ := Match("select", "SELECT")
	okT, qT, _ := Match("select", "sample_collection_target")
	if !okT {
		t.Fatal("precondition: Match(select, sample_collection_target) should be ok (weak but present)")
	}
	if qKw+KeywordSourceBias <= qT+SchemaSourceBias {
		t.Fatalf("precondition: composite kw %d not > composite schema %d; not a quality-beats-bias case",
			qKw+KeywordSourceBias, qT+SchemaSourceBias)
	}

	if got[0].Source != KeywordsSourceName || got[0].Text != "SELECT" {
		t.Fatalf("top = %q/%q; want keyword SELECT to outrank the weak schema match via composite Score",
			got[0].Source, got[0].Text)
	}
	iSchema := firstIndexBySource(got, SchemaSourceName)
	if iSchema <= 0 {
		t.Fatalf("schema table did not survive below the keyword (idx %d); want a real two-candidate race (got %v)",
			iSchema, suggestionTexts(got))
	}
}

// TestEngine_SuccessCriteria_UsrAndOeml pins the epic's end-to-end success
// criteria through Engine.Trigger: "usr" surfaces the table user_sessions
// (subsequence u-s-r) and "oeml" surfaces the column order_email
// (subsequence o-e-m-l), with the noise sources also wired so the assertion is
// on real merged Engine output.
func TestEngine_SuccessCriteria_UsrAndOeml(t *testing.T) {
	t.Run("usr->user_sessions", func(t *testing.T) {
		m := newFakeMeta()
		m.setTables("public", "user_sessions", "orders")
		m.setFunctions("now")
		schema := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
		store := &fakeHistoryRows{rows: []string{"SELECT 1"}}
		eng := NewEngine([]Source{
			schema,
			NewFunctionSource(m),
			KeywordsSource{PriorityVal: 20},
			HistorySource{Store: store, PriorityVal: 5},
		})
		b, p := bufWithCursor("SELECT * FROM usr")
		got := eng.Trigger(context.Background(), b, p)
		if !containsText(got, "user_sessions") {
			t.Fatalf("usr did not surface user_sessions; got %v", suggestionTexts(got))
		}
		if containsText(got, "orders") {
			t.Errorf("orders has no 'usr' subsequence and must be excluded; got %v", suggestionTexts(got))
		}
	})

	t.Run("oeml->order_email", func(t *testing.T) {
		m := newFakeMeta()
		m.setColumns("public", "orders", models.Column{Name: "order_email"}, models.Column{Name: "id"})
		schema := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
		eng := NewEngine([]Source{schema, KeywordsSource{PriorityVal: 20}})
		b, p := bufWithCursor("SELECT orders.oeml")
		got := eng.Trigger(context.Background(), b, p)
		if !containsText(got, "order_email") {
			t.Fatalf("oeml did not surface order_email; got %v", suggestionTexts(got))
		}
	})
}

// TestEngine_QualityFloor_OneCharOverlapExcluded pins the quality floor through
// the Engine: a multi-char prefix that shares only a single scattered char with
// the schema candidates produces NO schema suggestion in the merged output —
// Match's ok=false floor drops it before it ever reaches the Engine.
func TestEngine_QualityFloor_OneCharOverlapExcluded(t *testing.T) {
	m := newFakeMeta()
	m.setColumns("public", "orders", models.Column{Name: "shipped_at"})
	schema := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	eng := NewEngine([]Source{schema, KeywordsSource{PriorityVal: 20}})

	// "xq" is not a subsequence of any column -> excluded; no schema entry.
	b, p := bufWithCursor("SELECT orders.xq")
	got := eng.Trigger(context.Background(), b, p)
	if firstIndexBySource(got, SchemaSourceName) != -1 {
		t.Fatalf("a 1-char-overlap junk candidate reached the Engine output; want excluded (got %v)",
			suggestionTexts(got))
	}
}

// TestEngine_EmptyPrefix_AllContributingSourcesNonEmpty pins the empty-prefix
// edge: at a FROM context with an empty identifier prefix, every source that can
// legitimately fire there (schema tables + functions + keywords) contributes, so
// the popup is non-empty. History stays silent (its empty-prefix guard AND the
// schema-completable-context gate both apply) — that suppression is by design,
// not an empty popup.
func TestEngine_EmptyPrefix_AllContributingSourcesNonEmpty(t *testing.T) {
	m := newFakeMeta()
	m.setTables("public", "users", "orders")
	m.setFunctions("now", "lower")
	schema := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	store := &fakeHistoryRows{rows: []string{"SELECT 1"}}
	eng := NewEngine([]Source{
		schema,
		NewFunctionSource(m),
		KeywordsSource{PriorityVal: 20},
		HistorySource{Store: store, PriorityVal: 5},
	})

	b, p := bufWithCursor("SELECT * FROM ")
	got := eng.Trigger(context.Background(), b, p)
	if len(got) == 0 {
		t.Fatal("empty-prefix popup is empty; want non-empty (sources contribute their full lists)")
	}
	for _, src := range []string{SchemaSourceName, FunctionSourceName, KeywordsSourceName} {
		if firstIndexBySource(got, src) == -1 {
			t.Errorf("source %q did not contribute on empty prefix; got %v", src, suggestionTexts(got))
		}
	}
}

// TestEngine_DedupeAcrossSources_KeepsCompositeHigher proves the dedupe keeps
// the COMPOSITE-higher entry when two sources emit the same Text. A function
// contrivedly named "SELECT" matches the prefix "sel" at the same quality as the
// real SELECT keyword, but FunctionSourceBias (60) > KeywordSourceBias (40), so
// the surviving "SELECT" must be the function entry — and only once.
func TestEngine_DedupeAcrossSources_KeepsCompositeHigher(t *testing.T) {
	m := newFakeMeta()
	m.setFunctions("SELECT") // contrived collision with the SELECT keyword
	fn := NewFunctionSource(m)
	eng := NewEngine([]Source{KeywordsSource{PriorityVal: 20}, fn})

	b, p := bufWithCursor("sel")
	got := eng.Trigger(context.Background(), b, p)

	count, keptSource, keptScore := 0, "", 0
	for _, s := range got {
		if s.Text == "SELECT" {
			count++
			keptSource, keptScore = s.Source, s.Score
		}
	}
	if count != 1 {
		t.Fatalf("SELECT appears %d times; want exactly 1 after dedupe", count)
	}
	if keptSource != FunctionSourceName {
		t.Errorf("dedupe kept Source = %q (score %d); want function (composite-higher via 60>40 bias)",
			keptSource, keptScore)
	}
}

// containsText reports whether any suggestion has the exact Text t.
func containsText(got []Suggestion, t string) bool {
	for _, s := range got {
		if s.Text == t {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// cross-feature snippet interaction guard (engine half).
//
// A snippet whose Name collides on Text with a SQL keyword must dedupe
// deterministically by the rank constants — SnippetSourceBias (50) >
// KeywordSourceBias (40) — so the kept entry is the snippet (Body-bearing),
// not the keyword. This pins the "names unique" decision across sources
// (review-plan Finding S): the cross-source dedupe key is Text, and the
// composite Score, not source registration order, decides the winner.
// ---------------------------------------------------------------------------

// fixedSnippetProvider is a SnippetProvider returning a canned snippet set —
// used to force a Name that collides on Text with a keyword.
type fixedSnippetProvider struct{ snippets []Snippet }

func (f fixedSnippetProvider) Snippets() []Snippet { return f.snippets }

// TestEngine_SnippetOutranksCollidingKeyword proves the cross-source dedupe:
// a snippet named "SELECT" (Text "SELECT", composite q+50) and the SELECT
// keyword (Text "SELECT", composite q+40) collide on Text at the prefix
// "select"; the snippet outranks via the 50>40 bias so the single surviving
// "SELECT" entry is the snippet — Kind==snippet with a non-empty Body.
func TestEngine_SnippetOutranksCollidingKeyword(t *testing.T) {
	snip := NewSnippetSource(fixedSnippetProvider{snippets: []Snippet{
		{Name: "SELECT", Body: "SELECT *\nFROM table_name;"},
	}})
	eng := NewEngine([]Source{KeywordsSource{PriorityVal: 20}, snip})

	b, p := bufWithCursor("select")
	got := eng.Trigger(context.Background(), b, p)

	// Precondition: both sources match "select" at the SAME quality so the
	// bias gap (50 vs 40) — not match quality — is what decides the winner.
	_, qSnip, _ := Match("select", "SELECT")
	_, qKw, _ := Match("select", "SELECT")
	if qSnip != qKw {
		t.Fatalf("precondition: snippet quality %d != keyword quality %d; not a pure-bias race", qSnip, qKw)
	}

	count, kept := 0, Suggestion{}
	for _, s := range got {
		if s.Text == "SELECT" {
			count++
			kept = s
		}
	}
	if count != 1 {
		t.Fatalf("SELECT appears %d times; want exactly 1 after cross-source dedupe", count)
	}
	if kept.Source != SnippetSourceName {
		t.Errorf("dedupe kept Source = %q (score %d); want snippet (composite-higher via 50>40 bias)",
			kept.Source, kept.Score)
	}
	if kept.Kind != KindSnippet {
		t.Errorf("kept Kind = %q; want %q", kept.Kind, KindSnippet)
	}
	if kept.Body == "" {
		t.Errorf("kept Suggestion has empty Body; want the snippet expansion body preserved through dedupe")
	}
	if kept.Score != qSnip+SnippetSourceBias {
		t.Errorf("kept Score = %d; want %d (q+SnippetSourceBias)", kept.Score, qSnip+SnippetSourceBias)
	}
}

// TestEngine_EqualScoreCollisionDeterministicBySourceOrder pins the edge in
// the AC's "Edge & negative paths": when a snippet and a keyword collide on
// Text at EQUAL composite Score (snippet bias coerced to match the keyword's
// via PriorityVal is not enough — Score ties break on source Priority then
// registration order), the survivor is deterministic across runs. Here both
// the snippet source and keyword source are given equal Priority and the
// snippet's Score is forced equal to the keyword's by a contrived equal-bias
// stub, so the FIRST-registered source wins the tie deterministically.
func TestEngine_EqualScoreCollisionDeterministicBySourceOrder(t *testing.T) {
	// Two stub sources colliding on Text "x" at identical Score and Priority;
	// registration order is the only tiebreak, so "first" must win every run.
	first := &stubSource{name: "first", priority: 1, items: []Suggestion{
		{Text: "x", Display: "x (first)", Score: 5},
	}}
	second := &stubSource{name: "second", priority: 1, items: []Suggestion{
		{Text: "x", Display: "x (second)", Score: 5},
	}}
	eng := NewEngine([]Source{first, second})
	for i := 0; i < 50; i++ {
		got := eng.Trigger(context.Background(), nil, Position{})
		if len(got) != 1 {
			t.Fatalf("iter %d: len = %d; want 1 (deduped)", i, len(got))
		}
		if got[0].Source != "first" {
			t.Fatalf("iter %d: kept Source = %q; want %q (deterministic registration-order tiebreak)",
				i, got[0].Source, "first")
		}
	}
}
