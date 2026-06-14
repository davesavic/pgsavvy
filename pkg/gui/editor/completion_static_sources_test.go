package editor

import (
	"context"
	"sort"
	"testing"
)

// bufferFromLines builds a Buffer with the given lines and places the cursor
// at the end of the last line — the natural completion-trigger position.
func bufferFromLines(t *testing.T, lines ...string) (*Buffer, Position) {
	t.Helper()
	b := &Buffer{}
	b.Lines = make([]Line, len(lines))
	for i, l := range lines {
		b.Lines[i] = Line{Runes: []rune(l)}
	}
	pos := Position{Line: len(lines) - 1, Col: len([]rune(lines[len(lines)-1]))}
	b.Cursor = pos
	return b, pos
}

func TestKeywordsSource_FuzzyMatchSortedAscending(t *testing.T) {
	// Under the fuzzy matcher "SE" is a subsequence of more than
	// just SE-prefixed keywords (CASE, ELSE, …). What still holds: within-
	// source order stays ascending and the obvious prefix hits are present.
	// The Engine, not the source, floats the strongest matches to the top by
	// Score — so SELECT/SET ordering is no longer the source's concern.
	buf, pos := bufferFromLines(t, "SE")
	got := KeywordsSource{}.Suggest(context.Background(), buf, pos)

	if len(got) == 0 {
		t.Fatal("got 0 suggestions; want at least SELECT")
	}
	texts := make([]string, len(got))
	for i, s := range got {
		texts[i] = s.Text
		if s.Source != KeywordsSourceName {
			t.Errorf("got[%d].Source = %q; want %q", i, s.Source, KeywordsSourceName)
		}
	}
	if !sort.StringsAreSorted(texts) {
		t.Errorf("Suggest result not sorted ascending: %v", texts)
	}
	// SELECT and SET (the literal SE-prefix hits) must be present.
	want := map[string]bool{"SELECT": false, "SET": false}
	for _, txt := range texts {
		if _, ok := want[txt]; ok {
			want[txt] = true
		}
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("expected suggestion %q missing from result %v", k, texts)
		}
	}
	// The strong prefix hit SELECT must outscore a scattered subsequence hit
	// like CASE (boundary+prefix+contiguity bonuses beat a gapped match).
	scoreOf := func(text string) int {
		for _, s := range got {
			if s.Text == text {
				return s.Score
			}
		}
		t.Fatalf("keyword %q not in result %v", text, texts)
		return 0
	}
	if scoreOf("SELECT") <= scoreOf("CASE") {
		t.Errorf("SELECT Score (%d) should beat CASE Score (%d) — prefix hit > scattered subsequence", scoreOf("SELECT"), scoreOf("CASE"))
	}
}

func TestKeywordsSource_CaseInsensitivePrefix(t *testing.T) {
	// Lowercase "select" must still surface the uppercase SELECT keyword
	// (case-insensitive matching) as the highest-scoring keyword.
	buf, pos := bufferFromLines(t, "select")
	got := KeywordsSource{}.Suggest(context.Background(), buf, pos)
	if len(got) == 0 {
		t.Fatal("lowercase prefix produced 0 suggestions; want SELECT")
	}
	best := got[0]
	for _, s := range got[1:] {
		if s.Score > best.Score {
			best = s
		}
	}
	if best.Text != "SELECT" {
		t.Errorf("highest-scoring suggestion = %q; want SELECT (case-insensitive match)", best.Text)
	}
}

func TestKeywordsSource_EmptyPrefixReturnsAllSorted(t *testing.T) {
	buf, pos := bufferFromLines(t, " ") // cursor after a space → empty prefix
	got := KeywordsSource{}.Suggest(context.Background(), buf, pos)

	if len(got) != len(sqlKeywords) {
		t.Fatalf("len = %d; want full keyword list (%d)", len(got), len(sqlKeywords))
	}
	texts := make([]string, len(got))
	for i, s := range got {
		texts[i] = s.Text
	}
	if !sort.StringsAreSorted(texts) {
		t.Errorf("empty-prefix result not sorted ascending: %v", texts)
	}
}

func TestKeywordsSource_SubsequenceMatch(t *testing.T) {
	// "slt" is a non-prefix subsequence of SELECT (S-e-L-ec-T). The old
	// strings.HasPrefix filter would miss it; editor.Match surfaces it.
	buf, pos := bufferFromLines(t, "slt")
	got := KeywordsSource{}.Suggest(context.Background(), buf, pos)

	found := false
	for _, s := range got {
		if s.Text != "SELECT" {
			continue
		}
		found = true
		if s.Score <= KeywordSourceBias {
			t.Errorf("SELECT Score = %d; want > KeywordSourceBias (%d) for a real match", s.Score, KeywordSourceBias)
		}
		if len(s.Matches) != 3 {
			t.Errorf("SELECT Matches = %v; want 3 positions for subsequence 'slt'", s.Matches)
		}
	}
	if !found {
		t.Errorf("subsequence prefix 'slt' did not surface SELECT; got %d suggestions", len(got))
	}
}

func TestKeywordsSource_EmptyPrefixScoreIsBias(t *testing.T) {
	buf, pos := bufferFromLines(t, " ") // empty prefix
	got := KeywordsSource{}.Suggest(context.Background(), buf, pos)
	for _, s := range got {
		if s.Score != KeywordSourceBias {
			t.Errorf("%q Score = %d; want KeywordSourceBias (%d) on empty prefix", s.Text, s.Score, KeywordSourceBias)
		}
		if s.Matches != nil {
			t.Errorf("%q Matches = %v; want nil on empty prefix", s.Text, s.Matches)
		}
	}
}

func TestKeywordsSource_NoMatchReturnsEmpty(t *testing.T) {
	buf, pos := bufferFromLines(t, "ZZZZ_no_keyword_starts_with_this")
	got := KeywordsSource{}.Suggest(context.Background(), buf, pos)
	if len(got) != 0 {
		t.Fatalf("len = %d; want 0 for unmatched prefix; got %+v", len(got), got)
	}
}

func TestKeywordsSource_NilBufferReturnsAll(t *testing.T) {
	// Defensive: a nil buffer is treated as an empty prefix at a virtual
	// origin so the engine can still surface keywords if Z1 wires in a
	// fresh editor with no buffer yet.
	got := KeywordsSource{}.Suggest(context.Background(), nil, Position{})
	if len(got) != len(sqlKeywords) {
		t.Fatalf("nil buffer: len = %d; want full keyword list (%d)", len(got), len(sqlKeywords))
	}
}

func TestKeywordsSource_NameAndPriority(t *testing.T) {
	s := KeywordsSource{PriorityVal: 7}
	if s.Name() != KeywordsSourceName {
		t.Errorf("Name() = %q; want %q", s.Name(), KeywordsSourceName)
	}
	if s.Priority() != 7 {
		t.Errorf("Priority() = %d; want 7", s.Priority())
	}
}

func TestIdentifierPrefixAt_StopsAtNonWord(t *testing.T) {
	buf, _ := bufferFromLines(t, "SELECT * FROM us")
	// Cursor at end of "us".
	got := identifierPrefixAt(buf, Position{Line: 0, Col: 16})
	if got != "us" {
		t.Errorf("got %q; want %q", got, "us")
	}
}

func TestIdentifierPrefixAt_OutOfBounds(t *testing.T) {
	buf, _ := bufferFromLines(t, "abc")
	if got := identifierPrefixAt(buf, Position{Line: 5, Col: 0}); got != "" {
		t.Errorf("out-of-range line: got %q; want empty", got)
	}
	if got := identifierPrefixAt(buf, Position{Line: 0, Col: -1}); got != "" {
		t.Errorf("negative col: got %q; want empty", got)
	}
	// Col beyond line length should clamp, not panic.
	got := identifierPrefixAt(buf, Position{Line: 0, Col: 99})
	if got != "abc" {
		t.Errorf("col past EOL: got %q; want %q (clamped)", got, "abc")
	}
}
