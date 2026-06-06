package orchestrator_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// newCapturingLogger returns a DEBUG-level *slog.Logger that writes
// JSON-formatted lines to buf.
func newCapturingLogger(buf *bytes.Buffer) *slog.Logger {
	h := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(h)
}

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

// newRigWithLog mirrors editorTestRig wiring (master_editor_test.go) but
// injects a capturing session logger into the master editor. We can't
// rely on the existing helper because the WithSessionLog option must be
// supplied at construction time.
type loggedRig struct {
	matcher *keys.Matcher
	disp    orchestrator.Dispatcher
	buf     *bytes.Buffer
}

func newLoggedRig(t *testing.T, trieSet *keys.TrieSet, scope types.ContextKey, mode types.Mode) *loggedRig {
	t.Helper()
	store := keys.NewModeStore()
	store.Set(scope, mode)
	m, err := keys.NewMatcher(trieSet, keys.MatcherConfig{
		Modes:       store,
		TimeoutLen:  50 * time.Millisecond,
		TtimeoutLen: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	buf := &bytes.Buffer{}
	ed := orchestrator.NewMasterEditor(nil, m, scope, orchestrator.WithSessionLog(newCapturingLogger(buf)))
	disp, ok := ed.(orchestrator.Dispatcher)
	if !ok {
		t.Fatalf("master editor does not implement orchestrator.Dispatcher")
	}
	return &loggedRig{matcher: m, disp: disp, buf: buf}
}

func TestDispatch_EmitsKeyAndDispatchResultEvents(t *testing.T) {
	var mu sync.Mutex
	var fired []string
	cmd := &commands.Command{
		ID: "down",
		Handler: func(_ commands.ExecCtx) error {
			mu.Lock()
			fired = append(fired, "down")
			mu.Unlock()
			return nil
		},
	}
	ts := buildSingleBindingTrieSet([]keys.Key{{Code: 'j'}}, types.ModeNormal, types.QUERY_EDITOR, cmd)
	rig := newLoggedRig(t, ts, types.QUERY_EDITOR, types.ModeNormal)

	if _, err := rig.disp.Dispatch(nil, gocui.NewKeyRune('j')); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	keyEvs := findEvents(t, rig.buf, "key")
	if len(keyEvs) != 1 {
		t.Fatalf("key events = %d, want 1\nbuf=%s", len(keyEvs), rig.buf.String())
	}
	if keyEvs[0]["cat"] != "input" {
		t.Errorf("cat = %v, want input", keyEvs[0]["cat"])
	}
	if keyEvs[0]["scope"] != string(types.QUERY_EDITOR) {
		t.Errorf("scope = %v, want %s", keyEvs[0]["scope"], types.QUERY_EDITOR)
	}
	if keyEvs[0]["mode"] != types.ModeNormal.String() {
		t.Errorf("mode = %v, want %s", keyEvs[0]["mode"], types.ModeNormal.String())
	}
	if keyEvs[0]["key"] == "" || keyEvs[0]["key"] == "<redacted>" {
		t.Errorf("key = %v, expected a non-redacted keysym", keyEvs[0]["key"])
	}

	resultEvs := findEvents(t, rig.buf, "dispatch_result")
	if len(resultEvs) != 1 {
		t.Fatalf("dispatch_result events = %d, want 1\nbuf=%s", len(resultEvs), rig.buf.String())
	}
	if resultEvs[0]["result"] != "Dispatched" {
		t.Errorf("result = %v, want Dispatched", resultEvs[0]["result"])
	}
	if _, ok := resultEvs[0]["ms"].(float64); !ok {
		t.Errorf("ms missing or not numeric: %v", resultEvs[0]["ms"])
	}
}

func TestDispatch_FellThrough_EmitsDispatchResult(t *testing.T) {
	rig := newLoggedRig(t, keys.NewTrieSet(), types.QUERY_EDITOR, types.ModeNormal)

	// A non-printable Special key in normal mode without any binding
	// falls through.
	if _, err := rig.disp.Dispatch(nil, gocui.NewKeyName(gocui.KeyF1)); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	resultEvs := findEvents(t, rig.buf, "dispatch_result")
	if len(resultEvs) != 1 {
		t.Fatalf("dispatch_result events = %d, want 1\nbuf=%s", len(resultEvs), rig.buf.String())
	}
	if resultEvs[0]["result"] != "FellThrough" {
		t.Errorf("result = %v, want FellThrough", resultEvs[0]["result"])
	}
}

func TestDispatch_Passthrough_EmitsDispatchResult(t *testing.T) {
	rig := newLoggedRig(t, keys.NewTrieSet(), types.QUERY_EDITOR, types.ModeInsert)
	v := gocui.NewView("test", 0, 0, 10, 10, gocui.OutputNormal)
	if _, err := rig.disp.Dispatch(v, gocui.NewKeyRune('x')); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	resultEvs := findEvents(t, rig.buf, "dispatch_result")
	if len(resultEvs) != 1 {
		t.Fatalf("dispatch_result events = %d, want 1\nbuf=%s", len(resultEvs), rig.buf.String())
	}
	if resultEvs[0]["result"] != "Passthrough" {
		t.Errorf("result = %v, want Passthrough", resultEvs[0]["result"])
	}
}

// AD-21: a keystroke landing in a sensitive scope must emit the key
// event with the keysym replaced by "<redacted>". mode + scope remain.
func TestDispatch_RedactsKeysymInSensitiveScope(t *testing.T) {
	sensitiveScope := types.ContextKey("credentials_prompt")
	rig := newLoggedRig(t, keys.NewTrieSet(), sensitiveScope, types.ModeInsert)

	v := gocui.NewView("test", 0, 0, 10, 10, gocui.OutputNormal)
	if _, err := rig.disp.Dispatch(v, gocui.NewKeyRune('s')); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	keyEvs := findEvents(t, rig.buf, "key")
	if len(keyEvs) != 1 {
		t.Fatalf("key events = %d, want 1\nbuf=%s", len(keyEvs), rig.buf.String())
	}
	e := keyEvs[0]
	if e["key"] != "<redacted>" {
		t.Errorf("key = %v, want <redacted>", e["key"])
	}
	if e["scope"] != string(sensitiveScope) {
		t.Errorf("scope = %v, want %s", e["scope"], sensitiveScope)
	}
	if e["mode"] != types.ModeInsert.String() {
		t.Errorf("mode = %v, want %s", e["mode"], types.ModeInsert.String())
	}
}

// AD-21: redaction also applies in Normal mode (defensive — sensitive
// scope means redact regardless of mode).
func TestDispatch_RedactsKeysymInSensitiveScopeNormalMode(t *testing.T) {
	sensitiveScope := types.ContextKey("credentials_prompt")
	rig := newLoggedRig(t, keys.NewTrieSet(), sensitiveScope, types.ModeNormal)

	if _, err := rig.disp.Dispatch(nil, gocui.NewKeyRune('s')); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	keyEvs := findEvents(t, rig.buf, "key")
	if len(keyEvs) != 1 {
		t.Fatalf("key events = %d, want 1\nbuf=%s", len(keyEvs), rig.buf.String())
	}
	if keyEvs[0]["key"] != "<redacted>" {
		t.Errorf("key = %v, want <redacted> even in normal mode", keyEvs[0]["key"])
	}
}

// Ensure input-event fields never include Connection/DSN/Password
// (defensive — input scope has no path to credentials).
func TestInputEvents_DoNotIncludeConnectionFields(t *testing.T) {
	var mu sync.Mutex
	var fired []string
	cmd := &commands.Command{
		ID: "down",
		Handler: func(_ commands.ExecCtx) error {
			mu.Lock()
			fired = append(fired, "down")
			mu.Unlock()
			return nil
		},
	}
	ts := buildSingleBindingTrieSet([]keys.Key{{Code: 'j'}}, types.ModeNormal, types.QUERY_EDITOR, cmd)
	rig := newLoggedRig(t, ts, types.QUERY_EDITOR, types.ModeNormal)
	if _, err := rig.disp.Dispatch(nil, gocui.NewKeyRune('j')); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	forbidden := []string{"password", "dsn", "connection_string", "user:", "@", "postgres://"}
	body := strings.ToLower(rig.buf.String())
	for _, f := range forbidden {
		if strings.Contains(body, strings.ToLower(f)) {
			t.Errorf("emitted log body contains forbidden marker %q\nbody=%s", f, rig.buf.String())
		}
	}
}

// NilSessionLog: master editor must not panic when no logger is wired.
func TestDispatch_NilSessionLog_NoPanic(t *testing.T) {
	store := keys.NewModeStore()
	store.Set(types.QUERY_EDITOR, types.ModeNormal)
	m, err := keys.NewMatcher(keys.NewTrieSet(), keys.MatcherConfig{
		Modes:      store,
		TimeoutLen: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	// No WithSessionLog option.
	ed := orchestrator.NewMasterEditor(nil, m, types.QUERY_EDITOR)
	disp := ed.(orchestrator.Dispatcher)
	if _, err := disp.Dispatch(nil, gocui.NewKeyRune('j')); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
}
