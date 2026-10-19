package query

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestHistoryNew_WriteContention(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	for _, version := range []string{"current", "v1"} {
		t.Run(version, func(t *testing.T) {
			db, path := openTestDB(t)
			if version == "v1" {
				installHistoryV1(t, db)
			} else {
				require.NoError(t, migrate(db))
			}
			_, err := db.Exec(`BEGIN IMMEDIATE`)
			require.NoError(t, err)
			t.Cleanup(func() { _, _ = db.Exec(`ROLLBACK`) })
			type result struct {
				h   *History
				err error
			}
			done := make(chan result, 1)
			go func() {
				h, err := New(path)
				done <- result{h, err}
			}()
			var got result
			timedOut := false
			select {
			case got = <-done:
			case <-time.After(2 * time.Second):
				timedOut = true
			}
			// Release even on a regression so the constructor cannot leak a goroutine.
			_, err = db.Exec(`ROLLBACK`)
			require.NoError(t, err)
			if timedOut {
				got = <-done
			}
			if got.h != nil {
				require.NoError(t, got.h.Close())
			}
			require.False(t, timedOut, "a competing writer must not block TUI startup indefinitely")
			require.ErrorIs(t, got.err, context.DeadlineExceeded)
			require.Nil(t, got.h)
			h, err := New(path)
			require.NoError(t, err)
			require.NoError(t, h.Close(), "failed startup must release resources for reopening")
		})
	}
}

func TestHistoryAppend_CallerBudgetExceedsTUIStartupBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.sqlite")
	h, err := New(path)
	require.NoError(t, err)
	defer func() { _ = h.Close() }()
	_, err = h.db.Exec(`BEGIN IMMEDIATE`)
	require.NoError(t, err)
	defer func() { _, _ = h.db.Exec(`ROLLBACK`) }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- AppendHistory(ctx, path, appendTestEvent()) }()
	select {
	case err := <-done:
		t.Fatalf("CLI append abandoned the caller's budget: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	_, err = h.db.Exec(`ROLLBACK`)
	require.NoError(t, err)
	require.NoError(t, <-done)
}
