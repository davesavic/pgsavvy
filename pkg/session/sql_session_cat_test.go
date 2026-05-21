package session_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/logs"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/session"
)

// TestSQLSession_AllEventsCarryCatDB exercises every event flavour SQLSession
// emits (exec_start, exec_end, stream_start, stream_end, query_cancel,
// history_record) and asserts that every recorded slog.Record carries
// cat="db". This pins the AD-87v invariant: the deleted slog_bridge.go used
// to inject cat=db globally; SQLSession.New now pre-binds it on the stored
// logger, so the CategoryFilterHandler in the production handler chain can
// route session events to the file sink.
func TestSQLSession_AllEventsCarryCatDB(t *testing.T) {
	resetLogEnv(t)

	rh := logs.NewRecordingHandler()
	lg := slog.New(rh)

	conn := &fakeConn{}
	fs := &fakeSess{id: 42}
	s := session.New(conn, fs, session.Options{Logger: lg})
	t.Cleanup(func() { _ = s.Close() })

	// exec_start + exec_end + history_record
	fs.executeRes = models.Result{RowsAffected: 1}
	if _, err := s.Execute(context.Background(), models.Query{SQL: "UPDATE x"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// stream_start + stream_end
	staged := &fakeRowStream{qid: models.QueryID{SessionID: 42, Nonce: 1}, total: 1}
	fs.streams = []func() drivers.RowStream{func() drivers.RowStream { return staged }}
	runHandle, err := s.Stream(context.Background(), models.Query{SQL: "SELECT 1"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for {
		_, ok, _ := runHandle.Rows().Next(context.Background())
		if !ok {
			break
		}
	}
	<-runHandle.Done()
	_ = runHandle.Rows().Close()

	// query_cancel — issue another stream we can cancel.
	release := make(chan struct{})
	staged2 := &fakeRowStream{
		qid:     models.QueryID{SessionID: 42, BackendPID: 7, Nonce: 2},
		total:   1,
		blockOn: release,
	}
	fs.streams = []func() drivers.RowStream{func() drivers.RowStream { return staged2 }}
	rh2, err := s.Stream(context.Background(), models.Query{SQL: "SELECT pg_sleep(60)"})
	if err != nil {
		t.Fatalf("Stream(cancel): %v", err)
	}
	if err := s.Cancel(rh2.QueryID()); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	close(release)
	for {
		_, ok, _ := rh2.Rows().Next(context.Background())
		if !ok {
			break
		}
	}
	<-rh2.Done()
	_ = rh2.Rows().Close()

	wantEvts := map[string]bool{
		"exec_start":     false,
		"exec_end":       false,
		"stream_start":   false,
		"stream_end":     false,
		"query_cancel":   false,
		"history_record": false,
	}

	for _, rec := range rh.Records() {
		var cat string
		var evt string
		rec.Attrs(func(a slog.Attr) bool {
			switch a.Key {
			case "cat":
				cat = a.Value.String()
			case "evt":
				evt = a.Value.String()
			}
			return true
		})
		if _, tracked := wantEvts[evt]; tracked {
			if cat != "db" {
				t.Errorf("event %q missing cat=db (got cat=%q)", evt, cat)
			}
			wantEvts[evt] = true
		}
	}
	for evt, seen := range wantEvts {
		if !seen {
			t.Errorf("expected event %q not emitted", evt)
		}
	}
}
