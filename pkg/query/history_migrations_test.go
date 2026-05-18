package query

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

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
	require.Equal(t, 1, n)
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
