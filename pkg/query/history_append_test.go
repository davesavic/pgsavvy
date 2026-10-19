package query

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func appendTestEvent() HistoryEvent {
	return HistoryEvent{ExecutedAt: 12345, SQL: "SELECT 'retained literal' -- retained comment\n", DurationMS: 42, ConnectionID: "profile", Source: "cli", Outcome: "blocked"}
}

func TestHistoryAppend_FileURI(t *testing.T) {
	uri := historyFileURI("C:/Users/profile/history ?#.sqlite")
	require.Equal(t, "file:///C:/Users/profile/history%20%3F%23.sqlite?mode=rw", uri)
	u, err := url.Parse(uri)
	require.NoError(t, err)
	require.Empty(t, u.Host)
	require.Equal(t, "mode=rw", u.RawQuery)
}

func appendWithin(t *testing.T, path string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	return AppendHistory(ctx, path, appendTestEvent())
}

func TestHistoryAppend_ImmediatePersistence(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	path := filepath.Join(t.TempDir(), "state", "history ?#%.sqlite")
	require.NoError(t, appendWithin(t, path))
	// No TUI constructor, flush timer, or sleep participates in this read.
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "mode=rw"}).String())
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	rows, err := (&History{db: db}).Recent(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	event := appendTestEvent()
	require.Equal(t, HistoryRow{ID: 1, ExecutedAt: event.ExecutedAt, SQL: event.SQL, DurationMS: event.DurationMS, ConnectionID: event.ConnectionID, Source: event.Source, Outcome: event.Outcome}, rows[0])
	var integrity string
	require.NoError(t, db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity))
	require.Equal(t, "ok", integrity)
}

func TestHistoryAppend_ConcurrentStartup(t *testing.T) {
	for _, version := range []string{"fresh", "v1", "current"} {
		t.Run(version, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "history.sqlite")
			switch version {
			case "v1":
				makeAppendV1(t, path, 2)
			case "current":
				h, err := New(path)
				require.NoError(t, err)
				require.NoError(t, h.Close())
			}
			var commands []*exec.Cmd
			var starts []io.WriteCloser
			var outputs []*bytes.Buffer
			for range 2 {
				cmd, input, output := appendProcess(t, path, "append")
				commands = append(commands, cmd)
				starts = append(starts, input)
				outputs = append(outputs, output)
			}
			for _, input := range starts {
				_, err := io.WriteString(input, "start\n")
				require.NoError(t, err)
				require.NoError(t, input.Close())
			}
			for i, cmd := range commands {
				require.NoError(t, cmd.Wait(), outputs[i].String())
			}
			h, err := New(path)
			require.NoError(t, err)
			defer func() { _ = h.Close() }()
			rows, err := h.Recent(context.Background(), 10)
			require.NoError(t, err)
			want := 2
			if version == "v1" {
				want += 2
			}
			require.Len(t, rows, want)
			var versions int
			require.NoError(t, h.db.QueryRow(`SELECT COUNT(*) FROM _migrations`).Scan(&versions))
			require.Equal(t, 2, versions)
		})
	}
}

func appendProcess(t *testing.T, path, mode string) (*exec.Cmd, io.WriteCloser, *bytes.Buffer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHistoryAppend_ProcessHelper$")
	cmd.Env = append(os.Environ(), "PGSAVVY_APPEND_TEST_PATH="+path, "PGSAVVY_APPEND_TEST_MODE="+mode)
	input, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	output := &bytes.Buffer{}
	cmd.Stderr = output
	require.NoError(t, cmd.Start())
	// Each process has loaded its binary and is parked before opening the store.
	line, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "ready\n", line)
	return cmd, input, output
}

func TestHistoryAppend_ProcessHelper(t *testing.T) {
	path := os.Getenv("PGSAVVY_APPEND_TEST_PATH")
	if path == "" {
		return
	}
	fmt.Println("ready")
	_, err := bufio.NewReader(os.Stdin).ReadString('\n')
	require.NoError(t, err)
	switch os.Getenv("PGSAVVY_APPEND_TEST_MODE") {
	case "append":
		require.NoError(t, appendWithin(t, path))
	default:
		t.Fatal("unknown append fixture mode")
	}
}

func TestHistoryAppend_DeadlineAndLock(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	path := filepath.Join(t.TempDir(), "history.sqlite")
	h, err := New(path)
	require.NoError(t, err)
	defer func() { _ = h.Close() }()
	_, err = h.db.Exec(`BEGIN IMMEDIATE`)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	start := time.Now()
	err = AppendHistory(ctx, path, appendTestEvent())
	cancel()
	require.ErrorIs(t, err, ErrHistoryUnavailable)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotErrorIs(t, err, ErrHistoryUpgradeRequired)
	require.Less(t, time.Since(start), 200*time.Millisecond)
	_, err = h.db.Exec(`ROLLBACK`)
	require.NoError(t, err)
	require.NoError(t, appendWithin(t, path))
	rows, err := h.Recent(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	for _, expired := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		expected := context.Canceled
		if expired {
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			defer cancel()
			expected = context.DeadlineExceeded
		}
		missing := filepath.Join(t.TempDir(), "not-created", "history.sqlite")
		err = AppendHistory(ctx, missing, appendTestEvent())
		require.ErrorIs(t, err, expected)
		_, err = os.Stat(filepath.Dir(missing))
		require.ErrorIs(t, err, os.ErrNotExist)
	}
}

func makeAppendV1(t *testing.T, path string, count int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	installHistoryV1(t, db)
	if count > 0 {
		_, err = db.Exec(`WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n < ?)
INSERT INTO history(executed_at,sql,duration_ms,rows_affected,succeeded,connection_id)
SELECT n, 'SELECT legacy_' || n, n%7, n%11, n%2, 'legacy' FROM seq`, count)
		require.NoError(t, err)
	}
	require.NoError(t, db.Close())
	if runtime.GOOS != "windows" {
		require.NoError(t, os.Chmod(path, 0o600))
	}
}

func TestHistoryAppend_LargeV1Upgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.sqlite")
	makeAppendV1(t, path, 100_000)
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	before := sqliteRowsDigest(t, db, `SELECT * FROM history ORDER BY id`)
	_, err = db.Exec(`CREATE TRIGGER no_metadata_backfill BEFORE UPDATE ON history BEGIN SELECT RAISE(ABORT, 'backfill forbidden'); END`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	start := time.Now()
	require.NoError(t, appendWithin(t, path))
	t.Logf("bounded upgrade and append with 100000 rows: %s", time.Since(start))
	db, err = sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	require.Equal(t, before, sqliteRowsDigest(t, db, `SELECT id,executed_at,sql,duration_ms,rows_affected,succeeded,connection_id FROM history WHERE id<=100000 ORDER BY id`))
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM history_fts WHERE history_fts MATCH 'legacy'`).Scan(&count))
	require.Equal(t, 100_000, count)
}

func TestHistoryAppend_UpgradeBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.sqlite")
	makeAppendV1(t, path, 2)
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	// A deliberately expensive fixture-only marker trigger makes interruption
	// deterministic after the pending v2 migration has been established.
	_, err = db.Exec(`CREATE TRIGGER slow_upgrade BEFORE INSERT ON _migrations WHEN NEW.version=2 BEGIN
SELECT (WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<10000000) SELECT sum(x) FROM n); END`)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	start := time.Now()
	err = AppendHistory(ctx, path, appendTestEvent())
	cancel()
	require.ErrorIs(t, err, ErrHistoryUpgradeRequired)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 200*time.Millisecond)
	var columns int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('history') WHERE name IN ('source','blocked')`).Scan(&columns))
	require.Zero(t, columns, "interrupted DDL must roll back")
	_, err = db.Exec(`DROP TRIGGER slow_upgrade`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	require.NoError(t, appendWithin(t, path))
}

func TestHistoryAppend_ExistingTUIWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.sqlite")
	h, err := New(path)
	require.NoError(t, err)
	defer func() { _ = h.Close() }()
	h.Record("SELECT tui_before", 1, 2, true, "tui-profile")
	// Exercise the live writer lifecycle; Close below flushes partial batches.
	h.pauseWriter()
	h.resumeWriter()
	require.NoError(t, appendWithin(t, path))
	h.Record("SELECT tui_after", 1, 2, false, "tui-profile")
	require.NoError(t, h.Close())
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	rows, err := (&History{db: db}).Recent(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, rows, 3)
}

func TestHistoryAppend_InvalidStores(t *testing.T) {
	for _, fixture := range []string{"corrupt", "future", "directory", "symlink", "sidecar_symlink", "invalid_event", "uri"} {
		t.Run(fixture, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "history.sqlite")
			outside := filepath.Join(t.TempDir(), "untouched")
			require.NoError(t, os.WriteFile(outside, []byte("outside sentinel"), 0o600))
			event := appendTestEvent()
			switch fixture {
			case "corrupt":
				require.NoError(t, os.WriteFile(path, []byte("not sqlite: private sentinel"), 0o600))
			case "future":
				require.NoError(t, appendWithin(t, path))
				db, err := sql.Open("sqlite", path)
				require.NoError(t, err)
				_, err = db.Exec(`INSERT INTO _migrations VALUES (999)`)
				require.NoError(t, err)
				require.NoError(t, db.Close())
			case "directory":
				require.NoError(t, os.Mkdir(path, 0o700))
			case "symlink", "sidecar_symlink":
				if runtime.GOOS == "windows" {
					t.Skip("requires symlink privilege on Windows")
				}
				target := path
				if fixture == "sidecar_symlink" {
					target += "-wal"
				}
				require.NoError(t, os.Symlink(outside, target))
			case "invalid_event":
				event.Source = "invalid private source"
			case "uri":
				path = "file:" + path + "?mode=memory"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()
			err := AppendHistory(ctx, path, event)
			require.ErrorIs(t, err, ErrHistoryUnavailable)
			require.NotContains(t, err.Error(), dir)
			require.NotContains(t, err.Error(), "sentinel")
			require.NotContains(t, err.Error(), event.SQL)
			if fixture == "future" {
				require.ErrorIs(t, err, ErrIncompatible)
			}
			data, err := os.ReadFile(outside)
			require.NoError(t, err)
			require.Equal(t, "outside sentinel", string(data))
			if fixture == "invalid_event" {
				_, err := os.Stat(path)
				require.ErrorIs(t, err, os.ErrNotExist)
			}
		})
	}
}
