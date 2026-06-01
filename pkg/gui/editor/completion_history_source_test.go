package editor

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeHistoryStore captures the arguments to SearchByPrefix and replays a
// canned response so HistorySource can be tested without a SQLite file.
type fakeHistoryStore struct {
	gotPrefix string
	gotLimit  int
	rows      []string
	err       error
}

func (f *fakeHistoryStore) SearchByPrefix(_ context.Context, prefix string, limit int) ([]string, error) {
	f.gotPrefix = prefix
	f.gotLimit = limit
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

func TestHistorySource_PrefixPassThroughAndSource(t *testing.T) {
	store := &fakeHistoryStore{rows: []string{"SELECT 1", "SELECT 2"}}
	src := HistorySource{Store: store}
	buf, pos := bufferFromLines(t, "SEL")
	got := src.Suggest(context.Background(), buf, pos)

	if store.gotPrefix != "SEL" {
		t.Errorf("store called with prefix %q; want %q", store.gotPrefix, "SEL")
	}
	if store.gotLimit != DefaultHistoryLimit {
		t.Errorf("store called with limit %d; want %d", store.gotLimit, DefaultHistoryLimit)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d; want 2", len(got))
	}
	for i, s := range got {
		if s.Source != HistorySourceName {
			t.Errorf("got[%d].Source = %q; want %q", i, s.Source, HistorySourceName)
		}
		if s.Text != store.rows[i] {
			t.Errorf("got[%d].Text = %q; want %q", i, s.Text, store.rows[i])
		}
	}
}

func TestHistorySource_NilStoreReturnsEmpty(t *testing.T) {
	src := HistorySource{Store: nil}
	buf, pos := bufferFromLines(t, "SEL")
	got := src.Suggest(context.Background(), buf, pos)
	if got == nil {
		t.Fatal("Suggest returned nil; want empty non-nil slice")
	}
	if len(got) != 0 {
		t.Fatalf("len = %d; want 0 with nil store", len(got))
	}
}

func TestHistorySource_EmptyPrefixSkipsStore(t *testing.T) {
	store := &fakeHistoryStore{rows: []string{"SELECT 1"}}
	src := HistorySource{Store: store}
	buf, pos := bufferFromLines(t, " ") // cursor after a space → empty prefix
	got := src.Suggest(context.Background(), buf, pos)

	if store.gotPrefix != "" {
		t.Errorf("store called despite empty prefix (got %q)", store.gotPrefix)
	}
	if len(got) != 0 {
		t.Fatalf("len = %d; want 0", len(got))
	}
}

func TestHistorySource_StoreErrorReturnsEmpty(t *testing.T) {
	store := &fakeHistoryStore{err: errors.New("boom")}
	src := HistorySource{Store: store}
	buf, pos := bufferFromLines(t, "SEL")
	got := src.Suggest(context.Background(), buf, pos)
	if len(got) != 0 {
		t.Fatalf("len = %d; want 0 on store error (best-effort source)", len(got))
	}
}

func TestHistorySource_CustomLimitOverride(t *testing.T) {
	store := &fakeHistoryStore{rows: []string{"SELECT 1"}}
	src := HistorySource{Store: store, Limit: 3}
	buf, pos := bufferFromLines(t, "SEL")
	_ = src.Suggest(context.Background(), buf, pos)
	if store.gotLimit != 3 {
		t.Errorf("store called with limit %d; want 3 (Override)", store.gotLimit)
	}
}

func TestHistorySource_DisplayTruncatedAndNewlinesReplaced(t *testing.T) {
	long := "SELECT col_a, col_b, col_c FROM very_long_schema.very_long_table\nWHERE id = 1\nORDER BY 1"
	store := &fakeHistoryStore{rows: []string{long}}
	src := HistorySource{Store: store, DisplayWidth: 30}
	buf, pos := bufferFromLines(t, "SEL")
	got := src.Suggest(context.Background(), buf, pos)

	if len(got) != 1 {
		t.Fatalf("len = %d; want 1", len(got))
	}
	disp := got[0].Display
	if strings.ContainsAny(disp, "\n\r") {
		t.Errorf("Display contains raw newlines: %q", disp)
	}
	runes := []rune(disp)
	if len(runes) > 30 {
		t.Errorf("Display rune length = %d; want <= 30", len(runes))
	}
	if !strings.HasSuffix(disp, "…") {
		t.Errorf("Display %q does not end with ellipsis; expected truncation", disp)
	}
	if got[0].Text != long {
		t.Errorf("Text was modified; insertion must keep the original statement")
	}
}

func TestHistorySource_ShortStatementNotTruncated(t *testing.T) {
	store := &fakeHistoryStore{rows: []string{"SELECT 1"}}
	src := HistorySource{Store: store, DisplayWidth: 30}
	buf, pos := bufferFromLines(t, "SEL")
	got := src.Suggest(context.Background(), buf, pos)
	if got[0].Display != "SELECT 1" {
		t.Errorf("Display = %q; want %q", got[0].Display, "SELECT 1")
	}
}

// TestHistorySource_SuppressedInStructuredContext pins that history does
// NOT fire when the cursor sits in a schema-completable position (FROM /
// JOIN / ON / <ident>. / column context). Whole-statement history is only
// useful where schema completion has nothing to offer; in a structured
// position it would bury the relevant table/column hits.
func TestHistorySource_SuppressedInStructuredContext(t *testing.T) {
	lines := []string{
		"select * from posts join posts_summary on posts",
		"select * from ",
		"select * from posts join posts_summary on ",
		"select id from posts where ",
	}
	for _, line := range lines {
		store := &fakeHistoryStore{rows: []string{"SELECT * FROM posts"}}
		src := HistorySource{Store: store}
		buf, pos := bufferFromLines(t, line)
		got := src.Suggest(context.Background(), buf, pos)
		if len(got) != 0 {
			t.Errorf("history fired in structured context %q; got %d suggestions", line, len(got))
		}
		if store.gotPrefix != "" {
			t.Errorf("history queried store in structured context %q (prefix %q)", line, store.gotPrefix)
		}
	}
}

// TestHistorySource_FiresOutsideStructuredContext keeps history working at
// statement start, where re-running a past query is the point.
func TestHistorySource_FiresOutsideStructuredContext(t *testing.T) {
	store := &fakeHistoryStore{rows: []string{"SELECT * FROM posts"}}
	src := HistorySource{Store: store}
	buf, pos := bufferFromLines(t, "sel")
	got := src.Suggest(context.Background(), buf, pos)
	if len(got) != 1 {
		t.Fatalf("history suppressed at statement start; got %d, want 1", len(got))
	}
}

func TestHistorySource_NameAndPriority(t *testing.T) {
	s := HistorySource{PriorityVal: 2}
	if s.Name() != HistorySourceName {
		t.Errorf("Name() = %q; want %q", s.Name(), HistorySourceName)
	}
	if s.Priority() != 2 {
		t.Errorf("Priority() = %d; want 2", s.Priority())
	}
}

func TestTruncateForPopup_WidthZeroFallsBack(t *testing.T) {
	long := strings.Repeat("a", DefaultHistoryDisplayWidth+10)
	out := truncateForPopup(long, 0)
	if len([]rune(out)) != DefaultHistoryDisplayWidth {
		t.Errorf("len = %d; want %d (fallback width)", len([]rune(out)), DefaultHistoryDisplayWidth)
	}
}

func TestTruncateForPopup_CRLFCollapsesToSingleGlyph(t *testing.T) {
	in := "a\r\nb"
	out := truncateForPopup(in, 80)
	if out != "a"+historyReturnSymbol+"b" {
		t.Errorf("got %q; want %q", out, "a"+historyReturnSymbol+"b")
	}
}
