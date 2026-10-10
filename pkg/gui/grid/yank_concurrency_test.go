package grid

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/models"
)

type unlockedYankClipboard struct {
	view *View
	recordingClipboard
	rowsWhileWriting int
}

func (c *unlockedYankClipboard) Write(text string) error {
	c.view.mu.Lock()
	c.rowsWhileWriting = len(c.view.rows)
	c.view.mu.Unlock()
	return c.recordingClipboard.Write(text)
}

func TestFormattedYankWithPendingSettlement(t *testing.T) {
	const childEnv = "PGSAVVY_TEST_YANK_SETTLEMENT"
	if path := os.Getenv(childEnv); path != "" {
		testFormattedYankWithPendingSettlement(t, path)
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"selection", "row"} {
		t.Run(path, func(t *testing.T) {
			// A regressed nested read lock must not strand the test process.
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestFormattedYankWithPendingSettlement$")
			cmd.Env = append(os.Environ(), childEnv+"="+path)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("yank/settlement subprocess: %v\n%s", err, output)
			}
		})
	}
}

func testFormattedYankWithPendingSettlement(t *testing.T, path string) {
	t.Helper()
	v := makeCanonical3x3(t)
	v.SetColumns(catalogFixtureColumns(v.Columns()))
	v.AppendRows([]models.Row{{Values: []any{"a", "b", "c"}}, {Values: []any{"d", "e", "f"}}, {Values: []any{"g", "h", "i"}}})
	v.SetEditability(true, []int{0}, "", models.Ref{Schema: "test", Table: "rows"})
	cols := append([]models.ColumnMeta(nil), v.cols...)
	generation := v.CommitGeneration()
	clipboard := &unlockedYankClipboard{view: v}
	v.SetClipboard(clipboard)
	v.yankFormat = "tsv"
	locked, resume := make(chan struct{}), make(chan struct{})
	v.beforeYankSnapshot = func() {
		close(locked)
		<-resume
	}
	type result struct {
		value string
		ok    bool
		err   error
	}
	yanked := make(chan result, 1)
	go func() {
		var r result
		if path == "selection" {
			r.value, r.ok, r.err = v.YankSelection()
		} else {
			r.value, r.ok, r.err = v.YankRowWithHeaders()
		}
		yanked <- r
	}()
	select {
	case <-locked:
	case <-time.After(2 * time.Second):
		t.Fatal("yank did not reach its locked snapshot gate")
	}
	settled := make(chan struct{})
	go func() {
		v.SettleCommittedRows(generation, v.EditIdentity(), cols, []models.Row{{Values: []any{"a", "new-b", "new-c"}}}, [][]any{{"a"}}, true)
		close(settled)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for v.mu.TryRLock() {
		v.mu.RUnlock()
		if time.Now().After(deadline) {
			t.Fatal("settlement writer did not queue behind yank")
		}
		runtime.Gosched()
	}
	// A queued writer now blocks any recursive RLock by the yank goroutine.
	close(resume)
	select {
	case r := <-yanked:
		if r.err != nil || !r.ok || r.value != "c0\tc1\tc2\na\tb\tc" {
			t.Fatalf("yank lost its consistent pre-settlement snapshot: %+v", r)
		}
		if clipboard.lastWrite() != r.value || clipboard.rowsWhileWriting != 3 {
			t.Fatal("clipboard did not receive the same snapshot outside the grid lock")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("formatted yank deadlocked with a pending settlement writer")
	}
	select {
	case <-settled:
	case <-time.After(2 * time.Second):
		t.Fatal("settlement did not finish after yank")
	}
	rows := v.AllRows()
	if rows[0].Values[1] != "new-b" || rows[0].Values[2] != "new-c" || !v.Editable() {
		t.Fatal("committed row settlement was lost")
	}
}
