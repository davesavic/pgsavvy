package query

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"modernc.org/sqlite"
)

var (
	// ErrHistoryUnavailable deliberately carries no path or backend diagnostic.
	// Existing broad-access stores require manual permission remediation.
	ErrHistoryUnavailable = errors.New("query: history unavailable")
	// ErrHistoryUpgradeRequired means a known upgrade exhausted its budget.
	// Open the new TUI once to upgrade, then retry the CLI invocation.
	ErrHistoryUpgradeRequired = errors.New("query: history upgrade required")
)

// AppendHistory commits one event synchronously, without starting a TUI writer.
// The caller owns the total history-work budget; cancellation never starts a
// separate timeout or an abandoned writer. SQL literals/comments are retained.
func AppendHistory(ctx context.Context, path string, event HistoryEvent) (result error) {
	entry, err := historyEntryForEvent(event)
	if err != nil {
		return ErrHistoryUnavailable
	}
	db, err := openHistoryStore(ctx, path, true)
	if err != nil {
		return safeHistoryError(ctx, err)
	}
	defer func() {
		if err := db.Close(); err != nil && result == nil {
			result = ErrHistoryUnavailable
		}
	}()
	conn, err := db.Conn(ctx)
	if err != nil {
		return safeHistoryError(ctx, err)
	}
	defer func() { _ = conn.Close() }()
	const insert = `INSERT INTO history(executed_at,sql,duration_ms,rows_affected,succeeded,connection_id,source,blocked) VALUES (?,?,?,0,?,?,?,?)`
	err = historyExec(ctx, conn, insert, entry.executedAt, entry.sql, entry.durMs,
		entry.succeeded, entry.connID, entry.source, entry.blocked)
	if err != nil {
		return safeHistoryError(ctx, err)
	}
	return nil
}

func safeHistoryError(ctx context.Context, err error) error {
	kind := ErrHistoryUnavailable
	if errors.Is(err, ErrHistoryUpgradeRequired) {
		kind = ErrHistoryUpgradeRequired
	} else if errors.Is(err, ErrIncompatible) {
		return errors.Join(kind, ErrIncompatible)
	}
	if ctx.Err() != nil {
		return errors.Join(kind, ctx.Err())
	}
	return kind
}

func openHistoryStore(ctx context.Context, path string, private bool) (*sql.DB, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := prepareHistoryPath(path, private)
	if err != nil {
		return nil, ErrHistoryUnavailable
	}
	// Encode the filename, not caller-supplied SQLite URI options. mode=rw also
	// prevents SQLite from recreating a target removed after our private create.
	dsn := historyFileURI(path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	fail := func(err error) (*sql.DB, error) {
		_ = db.Close()
		return nil, err
	}
	if err := migrateContext(ctx, db); err != nil {
		return fail(err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return fail(err)
	}
	for _, statement := range []string{`PRAGMA journal_mode=WAL`, `PRAGMA synchronous=NORMAL`} {
		if err := historyExec(ctx, conn, statement); err != nil {
			_ = conn.Close()
			return fail(err)
		}
	}
	_ = conn.Close()
	return db, nil
}

func historyFileURI(path string) string {
	name := filepath.ToSlash(path)
	if !strings.HasPrefix(name, "/") {
		// A Windows drive must be a URI path, not an authority (file://C:/...).
		name = "/" + name
	}
	return (&url.URL{Scheme: "file", Path: name, RawQuery: "mode=rw"}).String()
}

func prepareHistoryPath(path string, private bool) (string, error) {
	if path == "" || strings.ContainsRune(path, 0) || strings.HasPrefix(path, "file:") || path == ":memory:" {
		return "", ErrHistoryUnavailable
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	dir, err := historyDirectory(filepath.Dir(path), private)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() || !safeHistoryDirectory(info, private) {
		return "", ErrHistoryUnavailable
	}
	path = filepath.Join(dir, filepath.Base(path))
	// Check every SQLite-managed sibling before opening SQLite: a journal or
	// WAL symlink is as dangerous as a database symlink. No permissions repair.
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		info, err := os.Lstat(path + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !safeHistoryFile(info, private) {
			return "", ErrHistoryUnavailable
		}
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		if err := file.Close(); err != nil {
			return "", err
		}
	} else if !errors.Is(err, os.ErrExist) {
		return "", err
	}
	info, err = os.Lstat(path)
	if err != nil || !safeHistoryFile(info, private) {
		return "", ErrHistoryUnavailable
	}
	return path, nil
}

func historyDirectory(dir string, private bool) (string, error) {
	ancestor := dir
	var missing []string
	for {
		_, err := os.Lstat(ancestor)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		missing = append(missing, filepath.Base(ancestor))
		ancestor = filepath.Dir(ancestor)
	}
	resolved, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", err
	}
	if err := validateHistoryAncestors(resolved, private); err != nil {
		return "", err
	}
	for i := len(missing) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, missing[i])
		if err := makeHistoryDirectory(resolved, private); err != nil {
			return "", err
		}
	}
	return resolved, nil
}

func validateHistoryAncestors(resolved string, private bool) error {
	// A protected leaf is insufficient if an untrusted ancestor owner/writer
	// can replace it between validation and SQLite's pathname-based open.
	// Root-owned sticky directories permit /tmp without permitting replacement
	// of our owned child. Same-user filesystem mutation is not isolated here.
	for parent := resolved; ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil || !info.IsDir() || !safeHistoryAncestor(info, private) {
			return ErrHistoryUnavailable
		}
		if filepath.Dir(parent) == parent {
			break
		}
	}
	return nil
}

func makeHistoryDirectory(path string, private bool) error {
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	// A concurrently appearing component must be checked before descending.
	// MkdirAll would accept an attacker-created parent in a sticky directory.
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || !safeHistoryDirectory(info, private) {
		return ErrHistoryUnavailable
	}
	return nil
}

// SQLite's default busy_timeout is zero. Retry only lock contention using the
// original context, rather than a driver busy handler with an additive timeout.
func historyExec(ctx context.Context, conn *sql.Conn, statement string, args ...any) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := conn.ExecContext(ctx, statement, args...)
		var sqliteErr *sqlite.Error
		if !errors.As(err, &sqliteErr) || (sqliteErr.Code()&0xff != 5 && sqliteErr.Code()&0xff != 6) {
			return err
		}
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
