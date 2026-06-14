package editor

import (
	"context"
	"strings"
	"testing"
)

// fakeSnippetProvider is a test double for SnippetProvider.
type fakeSnippetProvider struct {
	snippets []Snippet
}

func (f fakeSnippetProvider) Snippets() []Snippet { return f.snippets }

func TestSnippetSource_NameAndPriority(t *testing.T) {
	src := NewSnippetSource(fakeSnippetProvider{})
	if src.Name() != SnippetSourceName {
		t.Errorf("Name() = %q; want %q", src.Name(), SnippetSourceName)
	}
	if src.Priority() != SnippetSourcePriority {
		t.Errorf("Priority() = %d; want %d (SnippetSourcePriority)", src.Priority(), SnippetSourcePriority)
	}
	if SnippetSourcePriority != SnippetSourceBias {
		t.Errorf("SnippetSourcePriority = %d; want SnippetSourceBias = %d", SnippetSourcePriority, SnippetSourceBias)
	}
}

func TestSnippetSource_FuzzyNameMatch(t *testing.T) {
	body := "SELECT *\nFROM t\nWHERE c;"
	provider := fakeSnippetProvider{snippets: []Snippet{
		{Name: "select_all", Body: body},
		{Name: "delete_from", Body: "DELETE FROM t\nWHERE c;"},
	}}
	src := NewSnippetSource(provider)

	// "sa" is a subsequence of "select_all" (s..a..) but not "delete_from".
	buf, pos := bufferFromLines(t, "sa")
	got := src.Suggest(context.Background(), buf, pos)

	if len(got) != 1 {
		t.Fatalf("got %d suggestions; want 1 (select_all)", len(got))
	}
	s := got[0]
	if s.Display != "select_all" {
		t.Errorf("Display = %q; want select_all", s.Display)
	}
	if s.Text != "select_all" {
		t.Errorf("Text = %q; want select_all", s.Text)
	}
	if s.Source != SnippetSourceName {
		t.Errorf("Source = %q; want %q", s.Source, SnippetSourceName)
	}
	if SnippetSourceName != "snippets" {
		t.Errorf("SnippetSourceName = %q; want \"snippets\"", SnippetSourceName)
	}
	if s.Kind != KindSnippet {
		t.Errorf("Kind = %q; want %q", s.Kind, KindSnippet)
	}
	if s.Body != body {
		t.Errorf("Body = %q; want %q", s.Body, body)
	}
	if len(s.Matches) == 0 {
		t.Errorf("Matches is empty; want non-empty rune positions")
	}

	// Score must equal matchQuality + SnippetSourceBias.
	_, quality, positions := Match("sa", "select_all")
	if s.Score != quality+SnippetSourceBias {
		t.Errorf("Score = %d; want %d (quality %d + bias %d)", s.Score, quality+SnippetSourceBias, quality, SnippetSourceBias)
	}
	if len(s.Matches) != len(positions) {
		t.Errorf("Matches = %v; want %v", s.Matches, positions)
	}
	for i := range positions {
		if s.Matches[i] != positions[i] {
			t.Errorf("Matches[%d] = %d; want %d", i, s.Matches[i], positions[i])
			break
		}
	}
}

func TestSnippetSource_NilProviderEmptyNonNil(t *testing.T) {
	src := NewSnippetSource(nil)
	buf, pos := bufferFromLines(t, "se")
	got := src.Suggest(context.Background(), buf, pos)
	if got == nil {
		t.Fatal("Suggest returned nil; want empty non-nil slice")
	}
	if len(got) != 0 {
		t.Errorf("got %d suggestions; want 0", len(got))
	}
}

func TestSnippetSource_EmptyPrefixReturnsAll(t *testing.T) {
	provider := fakeSnippetProvider{snippets: []Snippet{
		{Name: "a", Body: "x\ny"},
		{Name: "b", Body: "x\ny"},
		{Name: "c", Body: "x\ny"},
	}}
	src := NewSnippetSource(provider)
	// Empty buffer line → empty prefix → Match("",x) matches all.
	buf, pos := bufferFromLines(t, "")
	got := src.Suggest(context.Background(), buf, pos)
	if len(got) != 3 {
		t.Fatalf("empty prefix got %d suggestions; want 3 (match-all)", len(got))
	}
}

func TestSnippetSource_NoMatchEmptySlice(t *testing.T) {
	provider := fakeSnippetProvider{snippets: []Snippet{
		{Name: "select_all", Body: "x\ny"},
	}}
	src := NewSnippetSource(provider)
	// "zzz" is not a subsequence of "select_all".
	buf, pos := bufferFromLines(t, "zzz")
	got := src.Suggest(context.Background(), buf, pos)
	if got == nil {
		t.Fatal("Suggest returned nil; want empty non-nil slice")
	}
	if len(got) != 0 {
		t.Errorf("got %d suggestions; want 0 for non-matching prefix", len(got))
	}
}

func TestSnippetSource_BuiltinStarterSet(t *testing.T) {
	got := BuiltinSnippetProvider{}.Snippets()
	if len(got) < 3 {
		t.Fatalf("BuiltinSnippetProvider returned %d snippets; want >= 3", len(got))
	}
	for i, sn := range got {
		if sn.Name == "" {
			t.Errorf("snippet[%d] has empty Name", i)
		}
		if !strings.Contains(sn.Body, "\n") {
			t.Errorf("snippet[%d] (%q) Body is not multi-line: %q", i, sn.Name, sn.Body)
		}
	}
}

// TestSnippetSource_OtherSourcesBodyZero confirms Suggestion.Body stays zero
// for the non-snippet sources (additive-field contract).
func TestSnippetSource_OtherSourcesBodyZero(t *testing.T) {
	buf, pos := bufferFromLines(t, "SE")
	for _, s := range (KeywordsSource{}).Suggest(context.Background(), buf, pos) {
		if s.Body != "" {
			t.Errorf("KeywordsSource emitted non-zero Body %q for %q", s.Body, s.Text)
		}
	}
}
