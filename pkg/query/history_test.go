package query

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
	_ "modernc.org/sqlite"
)

func TestHistory_RoundTrip(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	path := filepath.Join(t.TempDir(), "history.sqlite")
	h, err := New(path)
	require.NoError(t, err)

	h.Record("SELECT 1", 5, 1, true, "conn-a")
	require.NoError(t, h.Close())

	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	var n int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM history`).Scan(&n))
	require.Equal(t, 1, n)

	var (
		stmt   string
		dur    int64
		rows   int64
		ok     int
		connID string
		ts     int64
	)
	require.NoError(t, db.QueryRow(
		`SELECT sql, duration_ms, rows_affected, succeeded, connection_id, executed_at FROM history`,
	).Scan(&stmt, &dur, &rows, &ok, &connID, &ts))
	require.Equal(t, "SELECT 1", stmt)
	require.EqualValues(t, 5, dur)
	require.EqualValues(t, 1, rows)
	require.Equal(t, 1, ok)
	require.Equal(t, "conn-a", connID)
	require.NotZero(t, ts)
}

func TestHistory_FTSSearch(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	path := filepath.Join(t.TempDir(), "history.sqlite")
	h, err := New(path)
	require.NoError(t, err)

	h.Record("SELECT * FROM app.users WHERE id=1", 7, 1, true, "c1")
	h.Record("UPDATE orders SET total=0", 12, 3, true, "c1")
	require.NoError(t, h.Close())

	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	var rowid int
	require.NoError(t, db.QueryRow(
		`SELECT rowid FROM history_fts WHERE history_fts MATCH 'users'`,
	).Scan(&rowid))
	require.Greater(t, rowid, 0)

	var hits int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM history_fts WHERE history_fts MATCH 'orders'`,
	).Scan(&hits))
	require.Equal(t, 1, hits)
}

func TestHistory_DropOldestOnOverflow(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	path := filepath.Join(t.TempDir(), "history.sqlite")
	h, err := New(path)
	require.NoError(t, err)

	h.pauseWriter()

	const n = 200
	for i := 0; i < n; i++ {
		h.Record("SELECT 1", 1, 1, true, "c1")
	}

	dropped := h.Dropped()
	require.GreaterOrEqual(t, dropped, uint64(1))
	require.Less(t, dropped, uint64(n))

	h.resumeWriter()
	require.NoError(t, h.Close())

	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	var stored int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM history`).Scan(&stored))

	require.LessOrEqual(t, stored+int(dropped), n)
	require.LessOrEqual(t, stored, historyChanCap)
	require.Greater(t, stored, 0)
}

func TestHistory_CloseIdempotent(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	path := filepath.Join(t.TempDir(), "history.sqlite")
	h, err := New(path)
	require.NoError(t, err)

	require.NoError(t, h.Close())
	require.NoError(t, h.Close())
}

func TestHistory_AsSessionRecorder_ForwardsConnID(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	path := filepath.Join(t.TempDir(), "history.sqlite")
	h, err := New(path)
	require.NoError(t, err)

	rec := h.AsSessionRecorder("captured-conn")
	rec.Record("SELECT 42", 3, 0, true)
	require.NoError(t, h.Close())

	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	var (
		stmt   string
		connID string
	)
	require.NoError(t, db.QueryRow(
		`SELECT sql, connection_id FROM history`,
	).Scan(&stmt, &connID))
	require.Equal(t, "SELECT 42", stmt)
	require.Equal(t, "captured-conn", connID)
}

func TestHistory_ParentDirAutoCreate(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	nested := filepath.Join(t.TempDir(), "sub", "dir", "history.sqlite")
	h, err := New(nested)
	require.NoError(t, err)

	h.Record("SELECT 1", 1, 0, true, "x")
	require.NoError(t, h.Close())

	h2, err := New(nested)
	require.NoError(t, err)
	require.NoError(t, h2.Close())
}

func TestHistory_RecordNonBlockingLatency(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.sqlite")
	h, err := New(path)
	require.NoError(t, err)
	defer func() { _ = h.Close() }()

	h.pauseWriter()
	defer h.resumeWriter()

	start := time.Now()
	for i := 0; i < 1000; i++ {
		h.Record("SELECT 1", 1, 1, true, "c1")
	}
	elapsed := time.Since(start)

	require.Less(t, elapsed, time.Second,
		"1000 Record calls should complete well under 1s in the non-blocking path; got %v", elapsed)
}
