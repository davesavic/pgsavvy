package orchestrator_test

import (
	"context"
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/session"
)

// eofRowStream reports EOF on the first Next, so a ResultBufferManager
// worker drains an empty initial fill and then parks in its post-EOF
// chan loop (result_buffer_manager.go:416) waiting for a ReadRows
// request or Stop. This is exactly the parked-worker state captured in
// the production shutdown hang (goroutine 212246 in the SIGQUIT dump).
type eofRowStream struct{ qid models.QueryID }

func (eofRowStream) Columns() []models.ColumnMeta                   { return nil }
func (eofRowStream) Next(context.Context) (models.Row, bool, error) { return models.Row{}, false, nil }
func (eofRowStream) Close() error                                   { return nil }
func (s eofRowStream) QueryID() models.QueryID                      { return s.qid }
func (s eofRowStream) RowsAffected() int64                          { return 0 }

// fakeStreamSession embeds drivers.Session so only the two methods the
// stream-start path actually invokes need bodies; any other call panics
// (and must not happen, by construction).
type fakeStreamSession struct{ drivers.Session }

func (fakeStreamSession) ID() models.SessionID { return models.SessionID(1) }
func (fakeStreamSession) Stream(context.Context, models.Query) (drivers.RowStream, error) {
	return eofRowStream{qid: models.QueryID{SessionID: 1, Nonce: 1}}, nil
}

// fakeConn embeds drivers.Connection; the stream-start path needs none
// of its methods.
type fakeConn struct{ drivers.Connection }

// TestCloseDoesNotDeadlockWithParkedResultTabWorker reproduces the
// shutdown hang: a result tab whose stream reached EOF leaves its
// ResultBufferManager worker parked in the chan loop, holding a
// workersWG count. Gui.Close() must stop in-flight tab tasks BEFORE
// g.workersWG.Wait() or it blocks forever (the worker only exits when
// its per-task stopCh fires, which nothing does ahead of the Wait).
func TestCloseDoesNotDeadlockWithParkedResultTabWorker(t *testing.T) {
	g, _ := buildTestGui(t)

	sess := session.New(fakeConn{}, fakeStreamSession{}, session.Options{})
	rh, err := sess.Stream(context.Background(), models.Query{SQL: "SELECT 1"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	if err := g.ResultTabsHelper().OpenResultTab("q", rh); err != nil {
		t.Fatalf("OpenResultTab: %v", err)
	}

	// Wait for the worker to spin up and park (busy counter reaches 1).
	deadline := time.Now().Add(2 * time.Second)
	for g.BusyCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("worker never started (BusyCount stayed 0)")
		}
		time.Sleep(time.Millisecond)
	}

	done := make(chan error, 1)
	go func() { done <- g.Close() }()

	select {
	case <-done:
		// Close returned — no deadlock.
	case <-time.After(5 * time.Second):
		t.Fatal("Gui.Close() deadlocked: workersWG.Wait() never returned with a parked result-tab worker")
	}
}
