package session_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/session"
)

// newLoggedSession builds a SQLSession whose slog logger writes JSON lines
// into the returned buffer via a slog.JSONHandler. Tests can scan the
// buffer for `evt=...` lines after exercising the SQLSession.
func newLoggedSession(t *testing.T, opts ...func(*session.Options)) (*session.SQLSession, *fakeConn, *fakeSess, *syncBuf) {
	t.Helper()
	buf := &syncBuf{}
	// AD-87v: SQLSession.New now pre-binds cat="db" on the logger it stores,
	// so this fixture deliberately passes a logger WITHOUT cat=db. The test
	// assertions on `cat` therefore exercise the production path.
	lg := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	conn := &fakeConn{}
	sess := &fakeSess{id: 42}
	o := session.Options{Logger: lg}
	for _, f := range opts {
		f(&o)
	}
	s := session.New(conn, sess, o)
	t.Cleanup(func() { _ = s.Close() })
	return s, conn, sess, buf
}

// syncBuf is a goroutine-safe bytes.Buffer. slog may emit from multiple
// goroutines (stream notice fan-out etc.).
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// findEvents returns every parsed JSON object whose `evt` field equals
// want, in the order they appear in buf. Lines that fail to parse or have
// no matching evt are skipped.
func findEvents(t *testing.T, buf *syncBuf, want string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		if m["evt"] == want {
			out = append(out, m)
		}
	}
	return out
}

func TestExecute_EmitsStartAndEndEvents(t *testing.T) {
	resetLogEnv(t)
	s, _, fs, buf := newLoggedSession(t)
	fs.executeRes = models.Result{RowsAffected: 7}

	_, err := s.Execute(context.Background(), models.Query{SQL: "UPDATE x"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	starts := findEvents(t, buf, "exec_start")
	ends := findEvents(t, buf, "exec_end")
	if len(starts) != 1 {
		t.Fatalf("exec_start events = %d, want 1; buf=%s", len(starts), buf.String())
	}
	if len(ends) != 1 {
		t.Fatalf("exec_end events = %d, want 1; buf=%s", len(ends), buf.String())
	}
	if starts[0]["cat"] != "db" || ends[0]["cat"] != "db" {
		t.Errorf("missing cat=db; start=%v end=%v", starts[0], ends[0])
	}
	if got := starts[0]["sql_preview"]; got != "UPDATE x" {
		t.Errorf("sql_preview = %v, want UPDATE x", got)
	}
	if got := ends[0]["rows_affected"]; got != float64(7) {
		t.Errorf("rows_affected = %v, want 7", got)
	}
}

func TestStream_EmitsStartAndEndEvents(t *testing.T) {
	resetLogEnv(t)
	s, _, fs, buf := newLoggedSession(t)
	staged := &fakeRowStream{qid: models.QueryID{SessionID: 42, Nonce: 1}, total: 2}
	fs.streams = []func() drivers.RowStream{func() drivers.RowStream { return staged }}

	rh, err := s.Stream(context.Background(), models.Query{SQL: "SELECT 1"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	ctx := context.Background()
	for {
		_, ok, _ := rh.Rows().Next(ctx)
		if !ok {
			break
		}
	}
	<-rh.Done()
	_ = rh.Rows().Close()

	starts := findEvents(t, buf, "stream_start")
	ends := findEvents(t, buf, "stream_end")
	if len(starts) != 1 || len(ends) != 1 {
		t.Fatalf("starts=%d ends=%d, want 1 each; buf=%s", len(starts), len(ends), buf.String())
	}
	if got := ends[0]["rows_observed"]; got != float64(2) {
		t.Errorf("rows_observed = %v, want 2", got)
	}
}

func TestCancel_ConcurrentEmitsOnce(t *testing.T) {
	resetLogEnv(t)
	s, _, fs, buf := newLoggedSession(t)
	release := make(chan struct{})
	staged := &fakeRowStream{
		qid:     models.QueryID{SessionID: 42, BackendPID: 7, Nonce: 1},
		total:   1,
		blockOn: release,
	}
	fs.streams = []func() drivers.RowStream{func() drivers.RowStream { return staged }}

	rh, err := s.Stream(context.Background(), models.Query{SQL: "SELECT pg_sleep(60)"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.Cancel(rh.QueryID())
		}()
	}
	wg.Wait()

	cancels := findEvents(t, buf, "query_cancel")
	if len(cancels) != 1 {
		t.Errorf("query_cancel emits = %d, want 1; buf=%s", len(cancels), buf.String())
	}

	close(release)
	for {
		_, ok, _ := rh.Rows().Next(context.Background())
		if !ok {
			break
		}
	}
	<-rh.Done()
	_ = rh.Rows().Close()
}

func TestRecordHistory_EmitsHistoryEvent(t *testing.T) {
	resetLogEnv(t)
	s, _, fs, buf := newLoggedSession(t)
	fs.executeRes = models.Result{RowsAffected: 1}

	if _, err := s.Execute(context.Background(), models.Query{SQL: "INSERT"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	hits := findEvents(t, buf, "history_record")
	if len(hits) != 1 {
		t.Fatalf("history_record events = %d, want 1; buf=%s", len(hits), buf.String())
	}
	if got := hits[0]["sql_preview"]; got != "INSERT" {
		t.Errorf("sql_preview = %v", got)
	}
	if got := hits[0]["success"]; got != true {
		t.Errorf("success = %v, want true", got)
	}
}

func TestExecStart_ParamsHashedByDefault(t *testing.T) {
	resetLogEnv(t)
	s, _, _, buf := newLoggedSession(t)

	_, _ = s.Execute(context.Background(), models.Query{SQL: "X", Args: []any{"alice", "hunter2"}})
	starts := findEvents(t, buf, "exec_start")
	if len(starts) != 1 {
		t.Fatalf("starts = %d", len(starts))
	}
	if got := starts[0]["params_count"]; got != float64(2) {
		t.Errorf("params_count = %v, want 2", got)
	}
	hashes, ok := starts[0]["params_hashes"].([]any)
	if !ok {
		t.Fatalf("params_hashes not a slice: %T", starts[0]["params_hashes"])
	}
	if len(hashes) != 2 {
		t.Errorf("len(hashes) = %d, want 2", len(hashes))
	}
	for _, h := range hashes {
		hs, _ := h.(string)
		if hs == "hunter2" || hs == "alice" {
			t.Errorf("hash = %q is raw value", hs)
		}
		if len(hs) != 12 {
			t.Errorf("hash len = %d, want 12", len(hs))
		}
	}
	if strings.Contains(buf.String(), "hunter2") {
		t.Errorf("buf still contains raw password: %s", buf.String())
	}
}

func TestExecStart_ParamsVerbatimWithEnvOptIn(t *testing.T) {
	resetLogEnv(t)
	t.Setenv("DBSAVVY_LOG_INCLUDE_PARAMS", "1")
	s, _, _, buf := newLoggedSession(t)

	_, _ = s.Execute(context.Background(), models.Query{SQL: "X", Args: []any{"hunter2"}})
	if !strings.Contains(buf.String(), "hunter2") {
		t.Errorf("buf missing raw value when opt-in set: %s", buf.String())
	}
}

func TestSQLPreview_ConnectionPasswordScrubbed(t *testing.T) {
	resetLogEnv(t)
	s, _, _, buf := newLoggedSession(t, func(o *session.Options) {
		o.ConnectionPassword = "hunter2"
	})
	_, _ = s.Execute(context.Background(), models.Query{SQL: "SELECT 'hunter2'"})
	if strings.Contains(buf.String(), "hunter2") {
		t.Errorf("buf still contains password: %s", buf.String())
	}
}

// resetLogEnv unsets the AD-14 verbosity env vars for the duration of t.
func resetLogEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DBSAVVY_LOG_INCLUDE_SQL", "")
	t.Setenv("DBSAVVY_LOG_INCLUDE_PARAMS", "")
}
