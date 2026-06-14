package editor

import (
	"context"
	"slices"
	"testing"
)

func TestFunctionSource_Identity(t *testing.T) {
	src := NewFunctionSource(nil)
	if src.Name() != FunctionSourceName {
		t.Errorf("Name() = %q; want %q", src.Name(), FunctionSourceName)
	}
	if src.Priority() != FunctionSourcePriority {
		t.Errorf("Priority() = %d; want %d", src.Priority(), FunctionSourcePriority)
	}
}

func TestFunctionSource_NilMeta_Empty(t *testing.T) {
	src := NewFunctionSource(nil)
	got := src.Suggest(context.Background(), nil, Position{})
	if got == nil || len(got) != 0 {
		t.Fatalf("Suggest with nil meta = %+v; want empty non-nil", got)
	}
}

func TestFunctionSource_UnloadedFunctions_Empty(t *testing.T) {
	// meta exists but function names never warmed.
	src := NewFunctionSource(newFakeMeta())
	got := src.Suggest(context.Background(), nil, Position{})
	if got == nil || len(got) != 0 {
		t.Fatalf("Suggest with unloaded functions = %+v; want empty non-nil", got)
	}
}

func TestFunctionSource_ReturnsNamesWithCalleeDisplay(t *testing.T) {
	m := newFakeMeta()
	m.setFunctions("now", "lower", "upper")
	src := NewFunctionSource(m)

	got := src.Suggest(context.Background(), nil, Position{})
	if len(got) != 3 {
		t.Fatalf("len(got) = %d; want 3", len(got))
	}
	for i, want := range []string{"now", "lower", "upper"} {
		if got[i].Text != want {
			t.Errorf("got[%d].Text = %q; want %q", i, got[i].Text, want)
		}
		if got[i].Display != want+"(...)" {
			t.Errorf("got[%d].Display = %q; want %q", i, got[i].Display, want+"(...)")
		}
		if got[i].Source != FunctionSourceName {
			t.Errorf("got[%d].Source = %q; want %q", i, got[i].Source, FunctionSourceName)
		}
	}
}

// TestFunctionSource_ReflectsSnapshotChange: the source reads the snapshot every
// call (no internal cache), so a re-warm against the store is reflected on the
// next Suggest — and an empty (loaded) snapshot still returns empty.
func TestFunctionSource_ReflectsSnapshotChange(t *testing.T) {
	m := newFakeMeta()
	src := NewFunctionSource(m)

	if got := src.Suggest(context.Background(), nil, Position{}); len(got) != 0 {
		t.Fatalf("pre-load = %v; want empty", texts(got))
	}
	m.setFunctions("f1", "f2")
	got := texts(src.Suggest(context.Background(), nil, Position{}))
	if !equalStrings(got, []string{"f1", "f2"}) {
		t.Fatalf("post-load = %v; want [f1 f2]", got)
	}
}

func TestFunctionSource_EmptyLoadedReturnsEmpty(t *testing.T) {
	m := newFakeMeta()
	m.setFunctions() // explicit empty, loaded
	src := NewFunctionSource(m)
	got := src.Suggest(context.Background(), nil, Position{})
	if got == nil || len(got) != 0 {
		t.Fatalf("Suggest = %+v; want empty non-nil", got)
	}
}

func TestFunctionSource_SkipsEmptyNames(t *testing.T) {
	m := newFakeMeta()
	m.setFunctions("ok", "", "ok2")
	src := NewFunctionSource(m)

	got := src.Suggest(context.Background(), nil, Position{})
	if len(got) != 2 {
		t.Fatalf("len(got) = %d; want 2 (empty name filtered)", len(got))
	}
	if got[0].Text != "ok" || got[1].Text != "ok2" {
		t.Errorf("got = %+v; want [ok, ok2]", got)
	}
}

// suggestFnLine runs the function source against a single typed line, deriving
// the identifier prefix from the cursor position (mirrors suggestLine).
func suggestFnLine(src *FunctionSource, line string) []Suggestion {
	b, p := bufWithCursor(line)
	return src.Suggest(context.Background(), b, p)
}

// TestFunctionSource_FiltersByTypedPrefix: the source no longer returns the
// full function list — a typed identifier filters via editor.Match. Only the
// fuzzily-matching function survives (ek4 fix prerequisite: filtering).
func TestFunctionSource_FiltersByTypedPrefix(t *testing.T) {
	m := newFakeMeta()
	m.setFunctions("now", "lower", "upper")
	src := NewFunctionSource(m)

	got := suggestFnLine(src, "SELECT low")
	if !equalStrings(texts(got), []string{"lower"}) {
		t.Fatalf("got %v; want [lower] (filtered by typed prefix)", texts(got))
	}
}

// TestFunctionSource_CompositeScoreAndMatches: a filtered function carries a
// composite Score (matchQuality + FunctionSourceBias, strictly > 0) and the
// rune-offset Matches from the matcher — closing the ek4 Score=0 bug.
func TestFunctionSource_CompositeScoreAndMatches(t *testing.T) {
	m := newFakeMeta()
	m.setFunctions("lower")
	src := NewFunctionSource(m)

	got := suggestFnLine(src, "SELECT lo")
	if len(got) != 1 {
		t.Fatalf("got %v; want [lower]", texts(got))
	}
	ok, quality, positions := Match("lo", "lower")
	if !ok {
		t.Fatal("precondition: Match(lo, lower) should be ok")
	}
	if want := quality + FunctionSourceBias; got[0].Score != want {
		t.Errorf("Score = %d; want %d (matchQuality+FunctionSourceBias)", got[0].Score, want)
	}
	if got[0].Score <= 0 {
		t.Errorf("Score = %d; want > 0 (ek4 regression: functions must not be Score=0)", got[0].Score)
	}
	if !slices.Equal(got[0].Matches, positions) {
		t.Errorf("Matches = %v; want %v (rune offsets from matcher)", got[0].Matches, positions)
	}
}

// TestFunctionSource_EmptyPrefixBaseline: with no typed identifier, every
// function is offered at the baseline Score = FunctionSourceBias and Matches is
// nil (Match("", x) == (true, 0, nil) contract).
func TestFunctionSource_EmptyPrefixBaseline(t *testing.T) {
	m := newFakeMeta()
	m.setFunctions("now", "lower")
	src := NewFunctionSource(m)

	got := suggestFnLine(src, "SELECT ")
	if !equalStrings(texts(got), []string{"now", "lower"}) {
		t.Fatalf("got %v; want [now lower] (all at baseline)", texts(got))
	}
	for _, sg := range got {
		if sg.Score != FunctionSourceBias {
			t.Errorf("%q Score = %d; want %d (baseline)", sg.Text, sg.Score, FunctionSourceBias)
		}
		if sg.Matches != nil {
			t.Errorf("%q Matches = %v; want nil (empty prefix)", sg.Text, sg.Matches)
		}
	}
}

// TestFunctionSource_OneCharOverlapExcluded: a function whose name shares only a
// single scattered rune with a multi-char pattern is dropped by the matcher's
// quality floor (ok=false), not surfaced as junk.
func TestFunctionSource_OneCharOverlapExcluded(t *testing.T) {
	m := newFakeMeta()
	m.setFunctions("array_length")
	src := NewFunctionSource(m)

	// "xz" — 'x' never appears; not a subsequence at all → excluded.
	if got := suggestFnLine(src, "SELECT xz"); len(got) != 0 {
		t.Fatalf("got %v; want empty (non-subsequence excluded)", texts(got))
	}
	// Sanity: precondition that the matcher rejects a scattered low-quality
	// pattern that only overlaps by a single rune-class.
	if ok, _, _ := Match("zz", "array_length"); ok {
		t.Fatal("precondition: Match(zz, array_length) should be rejected")
	}
}

// TestEngine_FunctionsOutrankKeywords is the regression test
// (mirrors TestEngine_SchemaTablesOutrankKeywords). At comparable match quality
// a FUNCTION suggestion outranks a KEYWORD suggestion because FunctionSourceBias
// (60) > KeywordSourceBias (40). Before this fix the function emitted Score=0
// and sorted below every keyword.
//
// The prefix "se" is a genuine competition, not a walkover: it fuzzily matches
// the function "set_config" AND the real keywords SELECT and SET in sqlKeywords,
// all at identical match quality. So the function only reaches got[0] by winning
// the bias tie-break against surviving keyword candidates — which we assert.
func TestEngine_FunctionsOutrankKeywords(t *testing.T) {
	m := newFakeMeta()
	m.setFunctions("set_config")
	fn := NewFunctionSource(m)
	eng := NewEngine([]Source{KeywordsSource{PriorityVal: 20}, fn})

	b, p := bufWithCursor("SELECT se")
	got := eng.Trigger(context.Background(), b, p)
	if len(got) == 0 {
		t.Fatal("Trigger returned no suggestions")
	}

	// Precondition: the prefix must actually match both the function and at
	// least one keyword, otherwise the test degenerates into a walkover.
	if ok, _, _ := Match("se", "set_config"); !ok {
		t.Fatal("precondition: Match(se, set_config) should be ok")
	}
	if ok, _, _ := Match("se", "SELECT"); !ok {
		t.Fatal("precondition: Match(se, SELECT) should be ok")
	}

	// The function bias (60) must beat the keyword bias (40) at equal quality.
	if got[0].Source != FunctionSourceName {
		t.Fatalf("top suggestion Source = %q (text %q); want function to outrank keywords (ek4)",
			got[0].Source, got[0].Text)
	}

	// A keyword must ALSO survive the filter so this is a real competition: the
	// function wins the tie-break rather than being the only candidate.
	keywordSurvived := false
	keywordRankedLower := false
	for i, sg := range got {
		if sg.Source != KeywordsSourceName {
			continue
		}
		keywordSurvived = true
		if i > 0 {
			keywordRankedLower = true
		}
	}
	if !keywordSurvived {
		t.Fatalf("no keyword survived the filter; test is a walkover, not a competition (got %v)", got)
	}
	if !keywordRankedLower {
		t.Fatalf("a keyword tied/beat the function at got[0]; function bias did not win (got %v)", got)
	}
}
