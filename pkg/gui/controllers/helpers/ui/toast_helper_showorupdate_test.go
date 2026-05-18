package ui_test

import (
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
)

func TestToastShowOrUpdate_NewKeyEmitsToastAndTags(t *testing.T) {
	h := ui.NewToastHelper(nil)
	h.ShowOrUpdate("run-1", "hello", 0)
	if got := h.Current(); got != "hello" {
		t.Fatalf("Current = %q; want hello", got)
	}
	if got := h.CurrentKey(); got != "run-1" {
		t.Fatalf("CurrentKey = %q; want run-1", got)
	}
	hist := h.History()
	if len(hist) != 1 || hist[0] != "hello" {
		t.Fatalf("History = %v; want [hello]", hist)
	}
}

func TestToastShowOrUpdate_SameKeyReplacesMessageInPlace(t *testing.T) {
	h := ui.NewToastHelper(nil)
	h.ShowOrUpdate("run-1", "first", 0)
	h.ShowOrUpdate("run-1", "second", 0)
	if got := h.Current(); got != "second" {
		t.Fatalf("Current = %q; want second", got)
	}
	if got := h.CurrentKey(); got != "run-1" {
		t.Fatalf("CurrentKey = %q; want run-1", got)
	}
	hist := h.History()
	if len(hist) != 2 || hist[0] != "first" || hist[1] != "second" {
		t.Fatalf("History = %v; want [first second]", hist)
	}
}

func TestToastShowOrUpdate_DifferentKeyTagsNewToast(t *testing.T) {
	h := ui.NewToastHelper(nil)
	h.ShowOrUpdate("run-1", "alpha", 0)
	h.ShowOrUpdate("run-2", "beta", 0)
	if got := h.Current(); got != "beta" {
		t.Fatalf("Current = %q; want beta", got)
	}
	if got := h.CurrentKey(); got != "run-2" {
		t.Fatalf("CurrentKey = %q; want run-2", got)
	}
}

func TestToastShow_ClearsKey(t *testing.T) {
	h := ui.NewToastHelper(nil)
	h.ShowOrUpdate("run-1", "tagged", 0)
	h.Show("plain", 0)
	if got := h.CurrentKey(); got != "" {
		t.Fatalf("CurrentKey after plain Show = %q; want empty", got)
	}
}

func TestToastShowOrUpdate_EmptyKeyDelegatesToShow(t *testing.T) {
	h := ui.NewToastHelper(nil)
	h.ShowOrUpdate("", "plain", 0)
	if got := h.Current(); got != "plain" {
		t.Fatalf("Current = %q; want plain", got)
	}
	if got := h.CurrentKey(); got != "" {
		t.Fatalf("CurrentKey = %q; want empty", got)
	}
}

func TestToastClearResetsKey(t *testing.T) {
	h := ui.NewToastHelper(nil)
	h.ShowOrUpdate("run-1", "msg", 0)
	h.Clear()
	if got := h.CurrentKey(); got != "" {
		t.Fatalf("CurrentKey after Clear = %q; want empty", got)
	}
}

func TestToastShowOrUpdate_AfterAutoClearStartsFresh(t *testing.T) {
	d := &updateRecordingDriver{}
	h := ui.NewToastHelper(d)
	h.ShowOrUpdate("run-1", "first", 5*time.Millisecond)

	// Wait for the AfterFunc to enqueue the clear via driver.Update,
	// then flush it.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) && d.updates.Load() == 0 {
		time.Sleep(2 * time.Millisecond)
	}
	d.runUpdates(t)
	if got := h.Current(); got != "" {
		t.Fatalf("Current after auto-clear = %q; want empty", got)
	}
	if got := h.CurrentKey(); got != "" {
		t.Fatalf("CurrentKey after auto-clear = %q; want empty", got)
	}

	// A fresh ShowOrUpdate after auto-clear acts as a new tagged toast.
	h.ShowOrUpdate("run-1", "again", 0)
	if got := h.Current(); got != "again" {
		t.Fatalf("Current after refresh = %q; want again", got)
	}
	if got := h.CurrentKey(); got != "run-1" {
		t.Fatalf("CurrentKey after refresh = %q; want run-1", got)
	}
}
