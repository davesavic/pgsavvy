package keys

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// newCapturingLogger returns a DEBUG-level *slog.Logger that writes
// JSON-formatted lines to buf. Used by the cat=input event tests.
func newCapturingLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// findEvents returns every JSON line in buf whose evt field == name.
func findEvents(t *testing.T, buf *bytes.Buffer, name string) []map[string]any {
	t.Helper()
	out := []map[string]any{}
	for line := range strings.SplitSeq(buf.String(), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("invalid JSON line: %q: %v", line, err)
		}
		if m["evt"] == name {
			out = append(out, m)
		}
	}
	return out
}

func TestDispatch_EmitsChordResolvedEvents_UnambiguousLeaf(t *testing.T) {
	var mu sync.Mutex
	var fired []string
	cmd := recordingCmd("cmd.j", &fired, &mu, nil)
	ts := buildTrieSet(t, []trieEntry{
		{mode: types.ModeNormal, scope: types.QUERY_EDITOR, seq: []Key{{Code: 'j'}}, cmd: cmd},
	})
	m, err := NewMatcher(ts, MatcherConfig{
		Modes:      NewModeStore(),
		TimeoutLen: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	buf := &bytes.Buffer{}
	m.SetSessionLog(newCapturingLogger(buf))

	if _, err := m.Dispatch(types.QUERY_EDITOR, Key{Code: 'j'}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	evs := findEvents(t, buf, "chord_resolved")
	if len(evs) != 1 {
		t.Fatalf("chord_resolved events = %d, want 1\nbuf=%s", len(evs), buf.String())
	}
	e := evs[0]
	if e["cat"] != "input" {
		t.Errorf("cat = %v, want input", e["cat"])
	}
	if e["leaf"] != true {
		t.Errorf("leaf = %v, want true", e["leaf"])
	}
	if e["has_children"] != false {
		t.Errorf("has_children = %v, want false", e["has_children"])
	}
	if e["cmd_id"] != "cmd.j" {
		t.Errorf("cmd_id = %v, want cmd.j", e["cmd_id"])
	}
	if e["scope"] != string(types.QUERY_EDITOR) {
		t.Errorf("scope = %v, want %s", e["scope"], types.QUERY_EDITOR)
	}
	if e["seq"] == "" {
		t.Errorf("seq is empty")
	}
}

func TestDispatch_EmitsChordResolvedEvents_AmbiguousLeaf(t *testing.T) {
	var mu sync.Mutex
	var fired []string
	cmdG := recordingCmd("cmd.g", &fired, &mu, nil)
	cmdGG := recordingCmd("cmd.gg", &fired, &mu, nil)
	ts := buildTrieSet(t, []trieEntry{
		{mode: types.ModeNormal, scope: types.QUERY_EDITOR, seq: []Key{{Code: 'g'}}, cmd: cmdG},
		{mode: types.ModeNormal, scope: types.QUERY_EDITOR, seq: []Key{{Code: 'g'}, {Code: 'g'}}, cmd: cmdGG},
	})
	m, err := NewMatcher(ts, MatcherConfig{
		Modes:      NewModeStore(),
		TimeoutLen: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	buf := &bytes.Buffer{}
	m.SetSessionLog(newCapturingLogger(buf))

	if _, err := m.Dispatch(types.QUERY_EDITOR, Key{Code: 'g'}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	evs := findEvents(t, buf, "chord_resolved")
	if len(evs) != 1 {
		t.Fatalf("chord_resolved events = %d, want 1\nbuf=%s", len(evs), buf.String())
	}
	e := evs[0]
	if e["leaf"] != true || e["has_children"] != true {
		t.Errorf("leaf/has_children = %v/%v, want true/true", e["leaf"], e["has_children"])
	}
	if e["cmd_id"] != "cmd.g" {
		t.Errorf("cmd_id = %v, want cmd.g", e["cmd_id"])
	}
}

func TestDispatch_EmitsChordResolvedEvents_PureInterior(t *testing.T) {
	var mu sync.Mutex
	var fired []string
	cmdGG := recordingCmd("cmd.gg", &fired, &mu, nil)
	ts := buildTrieSet(t, []trieEntry{
		// Only the two-key sequence — first key is a pure interior node.
		{mode: types.ModeNormal, scope: types.QUERY_EDITOR, seq: []Key{{Code: 'g'}, {Code: 'g'}}, cmd: cmdGG},
	})
	m, err := NewMatcher(ts, MatcherConfig{
		Modes:      NewModeStore(),
		TimeoutLen: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	buf := &bytes.Buffer{}
	m.SetSessionLog(newCapturingLogger(buf))

	if _, err := m.Dispatch(types.QUERY_EDITOR, Key{Code: 'g'}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	evs := findEvents(t, buf, "chord_resolved")
	if len(evs) != 1 {
		t.Fatalf("chord_resolved events = %d, want 1\nbuf=%s", len(evs), buf.String())
	}
	e := evs[0]
	if e["leaf"] != false || e["has_children"] != true {
		t.Errorf("leaf/has_children = %v/%v, want false/true", e["leaf"], e["has_children"])
	}
	if e["cmd_id"] != "" {
		t.Errorf("cmd_id = %v, want empty for pure interior", e["cmd_id"])
	}
}

// TestDispatch_NoEvents_OnFellThrough confirms a dispatch that does not
// resolve through handleLookup does not synthesize chord_resolved
// (matcher.Dispatch FellThrough path returns BEFORE handleLookup).
func TestDispatch_NoEvents_OnFellThrough(t *testing.T) {
	m, err := NewMatcher(NewTrieSet(), MatcherConfig{
		Modes:      NewModeStore(),
		TimeoutLen: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	buf := &bytes.Buffer{}
	m.SetSessionLog(newCapturingLogger(buf))

	if _, err := m.Dispatch(types.QUERY_EDITOR, Key{Code: 'x'}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got := len(findEvents(t, buf, "chord_resolved")); got != 0 {
		t.Errorf("chord_resolved events = %d, want 0", got)
	}
}

// Ensure matcher.Dispatch is unaffected by a nil session log (defaults).
func TestDispatch_NilSessionLog_NoPanic(t *testing.T) {
	var mu sync.Mutex
	var fired []string
	cmd := recordingCmd("cmd.j", &fired, &mu, nil)
	ts := buildTrieSet(t, []trieEntry{
		{mode: types.ModeNormal, scope: types.QUERY_EDITOR, seq: []Key{{Code: 'j'}}, cmd: cmd},
	})
	m, err := NewMatcher(ts, MatcherConfig{
		Modes:      NewModeStore(),
		TimeoutLen: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	// SetSessionLog never called.
	if _, err := m.Dispatch(types.QUERY_EDITOR, Key{Code: 'j'}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
}

// Ensure a properly-shaped commands.Command satisfies cmdIDOf.
var _ = commands.Command{}
