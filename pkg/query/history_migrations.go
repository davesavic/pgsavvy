package query

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed sql/*.sql
var historySQLFS embed.FS

// ErrIncompatible is returned by migrate when the on-disk database has a
// schema version higher than the highest embedded migration. This signals
// that the binary is older than the database and refuses to touch it.
var ErrIncompatible = errors.New("query: history database has a newer schema than this binary supports")

// migrate applies any unapplied history_NNN.sql files to db inside per-file
// transactions and is idempotent across calls.
func migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS _migrations (version INTEGER PRIMARY KEY)`); err != nil {
		return fmt.Errorf("create _migrations: %w", err)
	}

	applied, err := loadAppliedVersions(db)
	if err != nil {
		return err
	}

	entries, err := loadEmbeddedMigrations()
	if err != nil {
		return err
	}

	highestEmbedded := 0
	for _, e := range entries {
		if e.version > highestEmbedded {
			highestEmbedded = e.version
		}
	}
	for v := range applied {
		if v > highestEmbedded {
			return fmt.Errorf("%w: on-disk version=%d, highest embedded=%d", ErrIncompatible, v, highestEmbedded)
		}
	}

	for _, e := range entries {
		if applied[e.version] {
			continue
		}
		if err := applyMigration(db, e); err != nil {
			return fmt.Errorf("apply migration %d: %w", e.version, err)
		}
	}

	return nil
}

type migrationEntry struct {
	version int
	name    string
	sql     string
}

func loadAppliedVersions(db *sql.DB) (map[int]bool, error) {
	rows, err := db.Query(`SELECT version FROM _migrations`)
	if err != nil {
		return nil, fmt.Errorf("select _migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	applied := make(map[int]bool)
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return applied, nil
}

func loadEmbeddedMigrations() ([]migrationEntry, error) {
	dir, err := fs.ReadDir(historySQLFS, "sql")
	if err != nil {
		return nil, fmt.Errorf("read embedded sql/: %w", err)
	}

	var entries []migrationEntry
	for _, f := range dir {
		if f.IsDir() {
			continue
		}
		name := f.Name()
		if !strings.HasPrefix(name, "history_") || !strings.HasSuffix(name, ".sql") {
			continue
		}
		verStr := strings.TrimSuffix(strings.TrimPrefix(name, "history_"), ".sql")
		v, err := strconv.Atoi(verStr)
		if err != nil {
			return nil, fmt.Errorf("bad migration filename %q: %w", name, err)
		}
		data, err := fs.ReadFile(historySQLFS, "sql/"+name)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		entries = append(entries, migrationEntry{version: v, name: name, sql: string(data)})
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].version < entries[j].version })
	return entries, nil
}

func applyMigration(db *sql.DB, e migrationEntry) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(e.sql); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`INSERT INTO _migrations(version) VALUES (?)`, e.version); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
