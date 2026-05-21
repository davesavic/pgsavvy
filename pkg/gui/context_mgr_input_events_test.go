package gui

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// newCapturingLogger returns a DEBUG-level *slog.Logger that writes
// JSON-formatted lines to buf. Used by the cat=input event tests.
func newCapturingLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func findEvents(t *testing.T, buf *bytes.Buffer, name string) []map[string]any {
	t.Helper()
	out := []map[string]any{}
	for _, line := range strings.Split(buf.String(), "\n") {
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

func TestContextTree_Push_EmitsCtxPushEvent(t *testing.T) {
	buf := &bytes.Buffer{}
	tree := NewContextTree()
	tree.SetSessionLog(newCapturingLogger(buf))

	schemas := newFake(types.SCHEMAS, types.SIDE_CONTEXT, nil, nil)
	mustPush(t, tree, schemas)

	evs := findEvents(t, buf, "ctx_push")
	if len(evs) != 1 {
		t.Fatalf("ctx_push events = %d, want 1\nbuf=%s", len(evs), buf.String())
	}
	e := evs[0]
	if e["cat"] != "input" {
		t.Errorf("cat = %v, want input", e["cat"])
	}
	if e["key"] != string(types.SCHEMAS) {
		t.Errorf("key = %v, want %s", e["key"], types.SCHEMAS)
	}
	if e["kind"] != "side" {
		t.Errorf("kind = %v, want side", e["kind"])
	}
	if e["stack_depth_before"].(float64) != 0 {
		t.Errorf("stack_depth_before = %v, want 0", e["stack_depth_before"])
	}
	if e["stack_depth_after"].(float64) != 1 {
		t.Errorf("stack_depth_after = %v, want 1", e["stack_depth_after"])
	}
}

func TestContextTree_Pop_EmitsCtxPopEvent(t *testing.T) {
	buf := &bytes.Buffer{}
	tree := NewContextTree()
	tree.SetSessionLog(newCapturingLogger(buf))

	schemas := newFake(types.SCHEMAS, types.SIDE_CONTEXT, nil, nil)
	popup := newFake(types.MENU, types.TEMPORARY_POPUP, nil, nil)
	mustPush(t, tree, schemas)
	mustPush(t, tree, popup)
	buf.Reset()

	if err := tree.Pop(); err != nil {
		t.Fatalf("Pop: %v", err)
	}

	evs := findEvents(t, buf, "ctx_pop")
	if len(evs) != 1 {
		t.Fatalf("ctx_pop events = %d, want 1\nbuf=%s", len(evs), buf.String())
	}
	e := evs[0]
	if e["key"] != string(types.MENU) {
		t.Errorf("key = %v, want %s", e["key"], types.MENU)
	}
	if e["kind"] != "temporary_popup" {
		t.Errorf("kind = %v, want temporary_popup", e["kind"])
	}
	if e["stack_depth_before"].(float64) != 2 || e["stack_depth_after"].(float64) != 1 {
		t.Errorf("stack depths = %v/%v, want 2/1", e["stack_depth_before"], e["stack_depth_after"])
	}
}

func TestContextTree_Replace_EmitsCtxReplaceEvent(t *testing.T) {
	buf := &bytes.Buffer{}
	tree := NewContextTree()
	tree.SetSessionLog(newCapturingLogger(buf))

	schemas := newFake(types.SCHEMAS, types.SIDE_CONTEXT, nil, nil)
	menu := newFake(types.MENU, types.TEMPORARY_POPUP, nil, nil)
	mustPush(t, tree, schemas)
	mustPush(t, tree, menu)
	buf.Reset()

	confirmation := newFake(types.CONFIRMATION, types.TEMPORARY_POPUP, nil, nil)
	if err := tree.Replace(confirmation); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	evs := findEvents(t, buf, "ctx_replace")
	if len(evs) != 1 {
		t.Fatalf("ctx_replace events = %d, want 1\nbuf=%s", len(evs), buf.String())
	}
	e := evs[0]
	if e["key"] != string(types.CONFIRMATION) {
		t.Errorf("key = %v, want %s", e["key"], types.CONFIRMATION)
	}
	if e["kind"] != "temporary_popup" {
		t.Errorf("kind = %v, want temporary_popup", e["kind"])
	}
	if e["stack_depth_before"].(float64) != 2 || e["stack_depth_after"].(float64) != 2 {
		t.Errorf("stack depths = %v/%v, want 2/2", e["stack_depth_before"], e["stack_depth_after"])
	}
}

// Pushing a SIDE_CONTEXT wipes the existing stack — wipeStack should
// emit a ctx_wipe event.
func TestContextTree_WipeStack_EmitsCtxWipeEvent(t *testing.T) {
	buf := &bytes.Buffer{}
	tree := NewContextTree()
	tree.SetSessionLog(newCapturingLogger(buf))

	schemas := newFake(types.SCHEMAS, types.SIDE_CONTEXT, nil, nil)
	main := newFake(types.QUERY_EDITOR, types.MAIN_CONTEXT, nil, nil)
	mustPush(t, tree, schemas)
	mustPush(t, tree, main)
	buf.Reset()

	tables := newFake(types.TABLES, types.SIDE_CONTEXT, nil, nil)
	mustPush(t, tree, tables)

	evs := findEvents(t, buf, "ctx_wipe")
	if len(evs) != 1 {
		t.Fatalf("ctx_wipe events = %d, want 1\nbuf=%s", len(evs), buf.String())
	}
	e := evs[0]
	if e["stack_depth_before"].(float64) != 2 || e["stack_depth_after"].(float64) != 0 {
		t.Errorf("wipe depths = %v/%v, want 2/0", e["stack_depth_before"], e["stack_depth_after"])
	}
}

// Pushing a MAIN_CONTEXT on top of an existing MAIN_CONTEXT routes
// through removeMain, which must emit a ctx_remove_main event.
func TestContextTree_RemoveMain_EmitsCtxRemoveMainEvent(t *testing.T) {
	buf := &bytes.Buffer{}
	tree := NewContextTree()
	tree.SetSessionLog(newCapturingLogger(buf))

	schemas := newFake(types.SCHEMAS, types.SIDE_CONTEXT, nil, nil)
	main := newFake(types.QUERY_EDITOR, types.MAIN_CONTEXT, nil, nil)
	mustPush(t, tree, schemas)
	mustPush(t, tree, main)
	buf.Reset()

	plan := newFake(types.PLAN, types.MAIN_CONTEXT, nil, nil)
	mustPush(t, tree, plan)

	evs := findEvents(t, buf, "ctx_remove_main")
	if len(evs) != 1 {
		t.Fatalf("ctx_remove_main events = %d, want 1\nbuf=%s", len(evs), buf.String())
	}
	e := evs[0]
	if e["key"] != string(types.QUERY_EDITOR) {
		t.Errorf("key = %v, want %s", e["key"], types.QUERY_EDITOR)
	}
	if e["kind"] != "main" {
		t.Errorf("kind = %v, want main", e["kind"])
	}
}

func TestContextTree_NilSessionLog_NoPanic(t *testing.T) {
	tree := NewContextTree()
	// SetSessionLog not called.
	schemas := newFake(types.SCHEMAS, types.SIDE_CONTEXT, nil, nil)
	mustPush(t, tree, schemas)
	confirm := newFake(types.CONFIRMATION, types.TEMPORARY_POPUP, nil, nil)
	mustPush(t, tree, confirm)
	if err := tree.Pop(); err != nil {
		t.Fatalf("Pop: %v", err)
	}
	if err := tree.Replace(newFake(types.MENU, types.TEMPORARY_POPUP, nil, nil)); err != nil {
		t.Fatalf("Replace: %v", err)
	}
}
