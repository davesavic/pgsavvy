package query

import (
	"context"
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

// migrate serializes schema decisions with other processes, including readers
// of the version table. A startup snapshot taken before BEGIN IMMEDIATE is stale.
func migrate(db *sql.DB) error {
	return migrateContext(context.Background(), db)
}

func migrateContext(ctx context.Context, db *sql.DB) (result error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if err := historyExec(ctx, conn, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	defer func() {
		// With busy_timeout=0, rollback never adds a lock-wait budget. It must
		// run even after ctx cancellation, before this connection is returned.
		_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
	}()
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS _migrations (version INTEGER PRIMARY KEY)`); err != nil {
		return fmt.Errorf("create _migrations: %w", err)
	}

	applied, err := loadAppliedVersionsContext(ctx, conn)
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
		defer func() {
			if result != nil && ctx.Err() != nil {
				result = errors.Join(ErrHistoryUpgradeRequired, ctx.Err())
			}
		}()
		if _, err := conn.ExecContext(ctx, e.sql); err != nil {
			return fmt.Errorf("apply migration %d: %w", e.version, err)
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO _migrations(version) VALUES (?)`, e.version); err != nil {
			return err
		}
	}

	return historyExec(ctx, conn, `COMMIT`)
}

type migrationEntry struct {
	version int
	name    string
	sql     string
}

type historyVersionReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func loadAppliedVersionsContext(ctx context.Context, db historyVersionReader) (map[int]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT version FROM _migrations`)
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
