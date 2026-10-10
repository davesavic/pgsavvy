package pg

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestReview_single_analyze_controlled_cancel_and_logs(t *testing.T) {
	dsn := os.Getenv("PGSAVVY_TEST_PG")
	if dsn == "" {
		t.Skip("requires isolated PGSAVVY_TEST_PG")
	}
	ctx, stop := context.WithTimeout(context.Background(), 20*time.Second)
	defer stop()
	observer, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("isolated observer connection failed")
	}
	t.Cleanup(func() { _ = observer.Close(context.Background()) })
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	const secret = "SINGLE_ANALYZE_SYNTHETIC_CREDENTIAL"
	role := pgx.Identifier{fmt.Sprintf("review_analyze_login_%d", time.Now().UnixNano())}.Sanitize()
	_, err = observer.Exec(ctx, "CREATE ROLE "+role+" LOGIN PASSWORD '"+secret+"'")
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := observer.Exec(cleanupCtx, "DROP OWNED BY "+role+" RESTRICT")
		require.NoError(t, err)
		_, err = observer.Exec(cleanupCtx, "DROP ROLE "+role)
		require.NoError(t, err)
	})
	parsed.User = url.UserPassword(strings.Trim(role, `"`), secret)
	buf := installCaptureLogger(t)
	drv, err := New(stubPrompter{})(ctx)
	require.NoError(t, err)
	conn, err := drv.Open(ctx, models.Connection{Name: "single-analyze", Driver: "postgres", DSN: parsed.String(), Password: secret}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	sess, err := conn.AcquireSession(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })
	res, err := sess.Execute(ctx, models.Query{SQL: "SELECT pg_backend_pid()"})
	require.NoError(t, err)
	pid := res.Rows[0].Values[0].(int32)
	key := time.Now().UnixNano()
	seq := pgx.Identifier{fmt.Sprintf("single_analyze_cancel_%d", key)}.Sanitize()
	_, err = observer.Exec(ctx, "CREATE SEQUENCE "+seq)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := observer.Exec(cleanupCtx, "DROP SEQUENCE "+seq)
		require.NoError(t, err)
	})
	_, err = observer.Exec(ctx, "GRANT USAGE, SELECT ON SEQUENCE "+seq+" TO "+role)
	require.NoError(t, err)
	_, err = observer.Exec(ctx, "SELECT pg_advisory_lock($1)", key)
	require.NoError(t, err)
	_, err = sess.Execute(ctx, models.Query{SQL: fmt.Sprintf(`CREATE FUNCTION pg_temp.single_analyze_wait() RETURNS int LANGUAGE plpgsql AS $$
BEGIN
  PERFORM nextval('%s');
  PERFORM pg_advisory_xact_lock(%d);
  RETURN 1;
END $$`, seq, key)})
	require.NoError(t, err)
	queryCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("ANALYZE worker did not settle")
		}
	})
	go func() {
		_, err := sess.Explain(queryCtx, models.Query{SQL: "SELECT pg_temp.single_analyze_wait()"}, true)
		done <- err
		close(done)
	}()
	// Cancel only once the backend has executed nextval and is visibly waiting
	// at the controlled lock barrier, not after an arbitrary sleep.
	for {
		var waiting bool
		err = observer.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_locks WHERE pid=$1 AND locktype='advisory' AND NOT granted)", pid).Scan(&waiting)
		require.NoError(t, err)
		if waiting {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("ANALYZE settled before reaching barrier: %v", err)
		case <-ctx.Done():
			t.Fatal("backend never reached lock barrier")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-ctx.Done():
		t.Fatal("cancelled ANALYZE failed to settle")
	}
	var advances int64
	require.NoError(t, observer.QueryRow(ctx, "SELECT last_value FROM "+seq).Scan(&advances))
	require.Equal(t, int64(1), advances)
	events := findEvents(t, buf, "explain")
	require.Len(t, events, 1)
	require.NotEmpty(t, events[0]["err"], "cancellation failure stays visible")
	// Context cancellation may retire pgx's connection; use a fresh checkout
	// for the independent driver-error redaction assertion.
	errorSession, err := conn.AcquireSession(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, errorSession.Close()) })
	_, err = errorSession.Explain(ctx, models.Query{SQL: "SELECT '" + secret + "'::integer"}, false)
	require.Error(t, err)
	var backendError *pgconn.PgError
	require.ErrorAs(t, err, &backendError)
	require.Equal(t, "22P02", backendError.Code)
	require.Contains(t, backendError.Message, secret, "returned backend diagnostic must remain unchanged")
	events = findEvents(t, buf, "explain")
	require.Len(t, events, 2)
	require.Contains(t, events[1]["err"], "***", "driver EXPLAIN logger must use resolved credential redaction")
	if strings.Contains(buf.String(), secret) {
		t.Error("synthetic credential leaked; captured log deliberately omitted")
	}
}

func TestReview_single_analyze_option_rejection_live(t *testing.T) {
	dsn := os.Getenv("PGSAVVY_TEST_PG")
	if dsn == "" {
		t.Skip("requires isolated PGSAVVY_TEST_PG")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("isolated fixture connection failed")
	}
	t.Cleanup(func() { require.NoError(t, conn.Close(ctx)) })
	_, err = conn.Exec(ctx, "CREATE TEMP SEQUENCE single_analyze_parser_seq")
	require.NoError(t, err)
	var version int
	require.NoError(t, conn.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&version))
	if version >= 120000 {
		// The legacy fixture exercises the rejection path; current servers
		// prove that all generated enriched options are accepted.
		for _, analyze := range []bool{false, true} {
			sql, _ := enrichedExplainSQL("SELECT 1", analyze)
			var raw []byte
			require.NoError(t, conn.QueryRow(ctx, sql).Scan(&raw))
		}
		return
	}
	for _, analyze := range []bool{false, true} {
		sql, _ := enrichedExplainSQL("SELECT nextval('single_analyze_parser_seq')", analyze)
		var raw []byte
		err = conn.QueryRow(ctx, sql).Scan(&raw)
		require.Error(t, err)
		require.True(t, isUnsupportedExplainOption(err, sql), "real option rejection must match the safety predicate")
		var called bool
		require.NoError(t, conn.QueryRow(ctx, "SELECT is_called FROM single_analyze_parser_seq").Scan(&called))
		require.False(t, called, "option error must precede target execution")
	}
}

func TestEnrichedExplainSQL(t *testing.T) {
	const sql = "SELECT 1"
	tests := []struct {
		name     string
		analyze  bool
		wantJSON string
		wantText string
	}{
		{
			name:     "analyze permitted enriches with ANALYZE/BUFFERS/VERBOSE/SETTINGS",
			analyze:  true,
			wantJSON: "EXPLAIN (ANALYZE, BUFFERS, VERBOSE, SETTINGS, FORMAT JSON) SELECT 1",
			wantText: "EXPLAIN ANALYZE SELECT 1",
		},
		{
			name:     "analyze denied is estimate-only with VERBOSE/SETTINGS",
			analyze:  false,
			wantJSON: "EXPLAIN (VERBOSE, SETTINGS, FORMAT JSON) SELECT 1",
			wantText: "EXPLAIN SELECT 1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotJSON, gotText := enrichedExplainSQL(sql, tt.analyze)
			if gotJSON != tt.wantJSON {
				t.Errorf("jsonSQL = %q, want %q", gotJSON, tt.wantJSON)
			}
			if gotText != tt.wantText {
				t.Errorf("textSQL = %q, want %q", gotText, tt.wantText)
			}
		})
	}
}

func TestBareExplainSQL(t *testing.T) {
	const sql = "SELECT 1"
	tests := []struct {
		name     string
		analyze  bool
		wantJSON string
		wantText string
	}{
		{
			name:     "analyze fallback",
			analyze:  true,
			wantJSON: "EXPLAIN (ANALYZE, FORMAT JSON) SELECT 1",
			wantText: "EXPLAIN ANALYZE SELECT 1",
		},
		{
			name:     "estimate fallback",
			analyze:  false,
			wantJSON: "EXPLAIN (FORMAT JSON) SELECT 1",
			wantText: "EXPLAIN SELECT 1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotJSON, gotText := bareExplainSQL(sql, tt.analyze)
			if gotJSON != tt.wantJSON {
				t.Errorf("jsonSQL = %q, want %q", gotJSON, tt.wantJSON)
			}
			if gotText != tt.wantText {
				t.Errorf("textSQL = %q, want %q", gotText, tt.wantText)
			}
		})
	}
}

func TestIsUnsupportedExplainOption(t *testing.T) {
	jsonSQL, _ := enrichedExplainSQL("SELECT 1", true)
	optionError := func() *pgconn.PgError {
		return &pgconn.PgError{Code: "42601", Message: `unrecognized EXPLAIN option "settings"`, Position: int32(strings.Index(jsonSQL, "SETTINGS") + 1)}
	}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "generic syntax error cannot prove no execution", err: &pgconn.PgError{Code: "42601"}, want: false},
		{name: "option parser rejection triggers fallback", err: optionError(), want: true},
		{name: "wrapped option parser rejection", err: errors.Join(errors.New("ctx"), optionError()), want: true},
		{name: "other pg error does not", err: &pgconn.PgError{Code: "42P01"}, want: false},
		{name: "plain error does not", err: errors.New("boom"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isUnsupportedExplainOption(tt.err, jsonSQL); got != tt.want {
				t.Errorf("isUnsupportedExplainOption(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestReview_single_analyze_fallback_boundary(t *testing.T) {
	for _, analyze := range []bool{false, true} {
		sql, _ := enrichedExplainSQL("SELECT settings FROM items", analyze)
		pos := int32(strings.Index(sql, "SETTINGS") + 1)
		for _, tc := range []struct {
			name, code, message, where, internal string
			position, internalPosition           int32
			want                                 bool
		}{
			{"option rejected before execution", "42601", `unrecognized EXPLAIN option "settings"`, "", "", pos, 0, true},
			{"runtime function", "42601", `unrecognized EXPLAIN option "settings"`, "PL/pgSQL function foo()", "", pos, 0, false},
			{"internal query", "42601", `unrecognized EXPLAIN option "settings"`, "", "SELECT bad", pos, 0, false},
			{"internal position", "42601", `unrecognized EXPLAIN option "settings"`, "", "", pos, 1, false},
			{"no position", "42601", `unrecognized EXPLAIN option "settings"`, "", "", 0, 0, false},
			{"target statement position", "42601", `unrecognized EXPLAIN option "settings"`, "", "", int32(len(sql)), 0, false},
			{"generic syntax error", "42601", `syntax error at or near "settings"`, "", "", pos, 0, false},
			{"other code", "XX000", `unrecognized EXPLAIN option "settings"`, "", "", pos, 0, false},
		} {
			t.Run(tc.name+sql[:pos], func(t *testing.T) {
				err := &pgconn.PgError{Code: tc.code, Message: tc.message, Where: tc.where, InternalQuery: tc.internal, Position: tc.position, InternalPosition: tc.internalPosition}
				if got := isUnsupportedExplainOption(err, sql); got != tc.want {
					t.Fatalf("fallback=%v, want %v", got, tc.want)
				}
			})
		}
	}
}
