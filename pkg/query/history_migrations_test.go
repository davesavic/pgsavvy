package query

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func openTestDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "h.sqlite")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

func TestMigrate_FreshDB_CreatesSchema(t *testing.T) {
	db, _ := openTestDB(t)

	require.NoError(t, migrate(db))

	names := readObjectNames(t, db, "table")
	require.Contains(t, names, "history")
	require.Contains(t, names, "_migrations")
	require.Contains(t, names, "history_fts")

	triggers := readObjectNames(t, db, "trigger")
	require.Contains(t, triggers, "history_ai")
	require.Contains(t, triggers, "history_ad")
	require.Contains(t, triggers, "history_au")
}

func TestMigrate_Idempotent(t *testing.T) {
	db, _ := openTestDB(t)

	require.NoError(t, migrate(db))
	require.NoError(t, migrate(db))

	var n int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM _migrations`).Scan(&n))
	require.Equal(t, 2, n)
}

func TestMigrate_FutureVersion_ReturnsErrIncompatible(t *testing.T) {
	db, _ := openTestDB(t)

	require.NoError(t, migrate(db))
	_, err := db.Exec(`INSERT INTO _migrations(version) VALUES (99)`)
	require.NoError(t, err)

	err = migrate(db)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrIncompatible), "want ErrIncompatible, got %v", err)
}

func installHistoryV1(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`PRAGMA journal_mode=WAL; CREATE TABLE _migrations (version INTEGER PRIMARY KEY)`)
	require.NoError(t, err)
	ddl, err := historySQLFS.ReadFile("sql/history_001.sql")
	require.NoError(t, err)
	require.NoError(t, applyMigration(db, migrationEntry{version: 1, sql: string(ddl)}))
}

func sqliteRowsDigest(t *testing.T, db *sql.DB, statement string) string {
	t.Helper()
	rows, err := db.Query(statement)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	require.NoError(t, err)
	values := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range ptrs {
		ptrs[i] = &values[i]
	}
	hash := sha256.New()
	for rows.Next() {
		require.NoError(t, rows.Scan(ptrs...))
		_, err := fmt.Fprintf(hash, "%#v\n", values)
		require.NoError(t, err)
	}
	require.NoError(t, rows.Err())
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func historyFTSDigests(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	out := make(map[string]string)
	for _, table := range []string{"history_fts_data", "history_fts_idx", "history_fts_docsize", "history_fts_config"} {
		out[table] = sqliteRowsDigest(t, db, "SELECT * FROM "+table+" ORDER BY 1,2")
	}
	return out
}

func TestHistory_MigrationV2LegacyWriter(t *testing.T) {
	for _, count := range []int{0, 2, 100_000} {
		t.Run(fmt.Sprintf("rows_%d", count), func(t *testing.T) {
			db, path := openTestDB(t)
			installHistoryV1(t, db)
			if count > 0 {
				_, err := db.Exec(`WITH RECURSIVE seq(n) AS (
    SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n < ?
)
INSERT INTO history(executed_at,sql,duration_ms,rows_affected,succeeded,connection_id)
SELECT n*1000, 'SELECT legacy_' || n, n%7, n%11, n%2, 'legacy' FROM seq`, count)
				require.NoError(t, err)
			}
			old, err := sql.Open("sqlite", path)
			require.NoError(t, err)
			defer func() { _ = old.Close() }()
			old.SetMaxOpenConns(1)
			insert, err := old.Prepare(`INSERT INTO history(executed_at,sql,duration_ms,rows_affected,succeeded,connection_id) VALUES (?,?,?,?,?,?)`)
			require.NoError(t, err)
			defer func() { _ = insert.Close() }()
			data := sqliteRowsDigest(t, db, `SELECT id,executed_at,sql,duration_ms,rows_affected,succeeded,connection_id FROM history ORDER BY id`)
			fts := historyFTSDigests(t, db)
			_, err = db.Exec(`CREATE TRIGGER forbid_history_backfill BEFORE UPDATE ON history BEGIN SELECT RAISE(ABORT, 'no history backfill'); END`)
			require.NoError(t, err)
			triggers := sqliteRowsDigest(t, db, `SELECT name,sql FROM sqlite_master WHERE type='trigger' ORDER BY name`)
			var before, after int64
			require.NoError(t, db.QueryRow(`SELECT total_changes()`).Scan(&before))
			start := time.Now()
			require.NoError(t, migrate(db))
			t.Logf("schema-only v2 upgrade over %d rows: %s", count, time.Since(start))
			require.NoError(t, db.QueryRow(`SELECT total_changes()`).Scan(&after))
			require.EqualValues(t, 1, after-before, "only the version marker may change")
			require.Equal(t, data, sqliteRowsDigest(t, db, `SELECT id,executed_at,sql,duration_ms,rows_affected,succeeded,connection_id FROM history ORDER BY id`))
			require.Equal(t, fts, historyFTSDigests(t, db), "FTS shadow storage changed")
			require.Equal(t, triggers, sqliteRowsDigest(t, db, `SELECT name,sql FROM sqlite_master WHERE type='trigger' ORDER BY name`))
			var wrong int
			require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM history WHERE source!='tui' OR blocked!=0`).Scan(&wrong))
			require.Zero(t, wrong)
			require.NoError(t, migrate(db))
			require.Equal(t, fts, historyFTSDigests(t, db))
			for _, succeeded := range []int{1, 0} {
				_, err := insert.Exec(987654, fmt.Sprintf("SELECT live_old_%d", succeeded), 17, 23, succeeded, "old-live")
				require.NoError(t, err)
			}
			h := &History{db: db}
			rows, err := h.Recent(context.Background(), 2)
			require.NoError(t, err)
			require.Len(t, rows, 2)
			for i, outcome := range []string{"failed", "succeeded"} {
				require.Equal(t, "tui", rows[i].Source)
				require.Equal(t, outcome, rows[i].Outcome)
				require.EqualValues(t, 987654, rows[i].ExecutedAt)
				require.EqualValues(t, 17, rows[i].DurationMS)
			}
			var matches int
			require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM history_fts WHERE history_fts MATCH 'legacy'`).Scan(&matches))
			require.Equal(t, count, matches)
			var integrity string
			require.NoError(t, db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity))
			require.Equal(t, "ok", integrity)
		})
	}
}

func TestHistory_MigrationV2Interrupted(t *testing.T) {
	db, _ := openTestDB(t)
	installHistoryV1(t, db)
	_, err := db.Exec(`CREATE TRIGGER interrupt_v2 BEFORE INSERT ON _migrations WHEN NEW.version=2 BEGIN SELECT RAISE(ABORT, 'interrupted after DDL'); END`)
	require.NoError(t, err)
	for range 2 {
		require.ErrorContains(t, migrate(db), "interrupted after DDL")
		var columns, versions int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('history') WHERE name IN ('source','blocked')`).Scan(&columns))
		require.Zero(t, columns)
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM _migrations`).Scan(&versions))
		require.Equal(t, 1, versions)
	}
	_, err = db.Exec(`DROP TRIGGER interrupt_v2`)
	require.NoError(t, err)
	require.NoError(t, migrate(db))
}

func TestHistory_MigrationRollback(t *testing.T) {
	db, _ := openTestDB(t)
	installHistoryV1(t, db)
	_, err := db.Exec(`INSERT INTO history(id,executed_at,sql,duration_ms,rows_affected,succeeded,connection_id) VALUES (42,1234,'SELECT preserved_token',7,9,1,'legacy')`)
	require.NoError(t, err)
	require.NoError(t, migrate(db))
	_, err = db.Exec(`INSERT INTO history(executed_at,sql,duration_ms,rows_affected,succeeded,connection_id,source,blocked) VALUES (5678,'UPDATE blocked_token',3,0,0,'cli-profile','cli',1)`)
	require.NoError(t, err)
	data := sqliteRowsDigest(t, db, `SELECT id,executed_at,sql,duration_ms,rows_affected,succeeded,connection_id FROM history ORDER BY id`)
	fts := historyFTSDigests(t, db)
	// SQLite creates a consistent backup including committed WAL content.
	backup := filepath.Join(t.TempDir(), "backup.sqlite")
	_, err = db.Exec(`VACUUM INTO ?`, backup)
	require.NoError(t, err)
	copyDB, err := sql.Open("sqlite", backup)
	require.NoError(t, err)
	defer func() { _ = copyDB.Close() }()
	copyDB.SetMaxOpenConns(1)
	down, err := historySQLFS.ReadFile("sql/rollback_002.sql")
	require.NoError(t, err)
	_, err = copyDB.Exec(string(down))
	require.NoError(t, err)
	var columns, version int
	require.NoError(t, copyDB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('history')`).Scan(&columns))
	require.Equal(t, 7, columns)
	require.NoError(t, copyDB.QueryRow(`SELECT MAX(version) FROM _migrations`).Scan(&version))
	require.Equal(t, 1, version)
	require.Equal(t, data, sqliteRowsDigest(t, copyDB, `SELECT id,executed_at,sql,duration_ms,rows_affected,succeeded,connection_id FROM history ORDER BY id`))
	require.Equal(t, fts, historyFTSDigests(t, copyDB))
	var hits int
	require.NoError(t, copyDB.QueryRow(`SELECT COUNT(*) FROM history_fts WHERE history_fts MATCH 'preserved_token OR blocked_token'`).Scan(&hits))
	require.Equal(t, 2, hits)
	_, err = copyDB.Exec(`INSERT INTO history(executed_at,sql,duration_ms,rows_affected,succeeded,connection_id) VALUES (9012,'SELECT old_write_after_down',1,2,0,'predecessor')`)
	require.NoError(t, err)
	require.NoError(t, migrate(copyDB))
	rows, err := (&History{db: copyDB}).Recent(context.Background(), 3)
	require.NoError(t, err)
	require.Len(t, rows, 3)
	require.EqualValues(t, 42, rows[2].ID)
	require.EqualValues(t, 1234, rows[2].ExecutedAt)
	require.Equal(t, "SELECT preserved_token", rows[2].SQL)
	require.Equal(t, "failed", rows[0].Outcome)
	require.Equal(t, "failed", rows[1].Outcome, "blocked distinction is intentionally lost")
	for _, row := range rows {
		require.Equal(t, "tui", row.Source, "source distinction is intentionally lost")
	}
	// A second down on v1, or a down from a future schema, is refused atomically.
	for _, future := range []bool{false, true} {
		t.Run(fmt.Sprintf("reject_future_%t", future), func(t *testing.T) {
			target, _ := openTestDB(t)
			installHistoryV1(t, target)
			if future {
				require.NoError(t, migrate(target))
				_, err := target.Exec(`INSERT INTO _migrations VALUES (3)`)
				require.NoError(t, err)
			}
			before := sqliteRowsDigest(t, target, `SELECT name,sql FROM sqlite_master ORDER BY name`)
			_, err := target.Exec(string(down))
			require.ErrorContains(t, err, "exact v2 schema")
			require.Equal(t, before, sqliteRowsDigest(t, target, `SELECT name,sql FROM sqlite_master ORDER BY name`))
		})
	}
}

func readObjectNames(t *testing.T, db *sql.DB, typ string) map[string]bool {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = ?`, typ)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		out[n] = true
	}
	require.NoError(t, rows.Err())
	return out
}
