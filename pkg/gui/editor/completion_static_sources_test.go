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

func TestKeywordsSource_PrefixMatchSortedAscending(t *testing.T) {
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
	// SELECT and SET both start with SE; make sure they're both present.
	want := map[string]bool{"SELECT": false, "SET": false}
	for _, txt := range texts {
		if _, ok := want[txt]; ok {
			want[txt] = true
		}
		if len(txt) < 2 || txt[:2] != "SE" {
			t.Errorf("non-matching suggestion %q in SE-prefix result", txt)
		}
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("expected suggestion %q missing from result %v", k, texts)
		}
	}
}

func TestKeywordsSource_CaseInsensitivePrefix(t *testing.T) {
	buf, pos := bufferFromLines(t, "se")
	got := KeywordsSource{}.Suggest(context.Background(), buf, pos)
	if len(got) == 0 {
		t.Fatal("lowercase prefix produced 0 suggestions; want SELECT et al.")
	}
	if got[0].Text != "SELECT" {
		// Alphabetical: SELECT comes before SET.
		t.Errorf("first suggestion = %q; want SELECT (alphabetical first SE* keyword)", got[0].Text)
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

func TestSnippetsStubSource_AlwaysEmpty(t *testing.T) {
	buf, pos := bufferFromLines(t, "anything")
	got := SnippetsStubSource{}.Suggest(context.Background(), buf, pos)
	if got == nil {
		t.Fatal("Suggest returned nil; want empty non-nil slice")
	}
	if len(got) != 0 {
		t.Fatalf("len = %d; want 0 (stub)", len(got))
	}
	stub := SnippetsStubSource{}
	if stub.Name() != SnippetsStubSourceName {
		t.Errorf("Name() = %q; want %q", stub.Name(), SnippetsStubSourceName)
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
