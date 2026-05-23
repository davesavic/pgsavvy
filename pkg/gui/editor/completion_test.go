package editor

import (
	"context"
	"testing"
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
