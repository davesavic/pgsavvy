package query

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/davesavic/pgsavvy/pkg/session"

	_ "modernc.org/sqlite"
)

const (
	historyChanCap     = 128
	historyBatchSize   = 50
	historyFlushPeriod = 100 * time.Millisecond
)

// History persists executed statements to a SQLite file with FTS5 search on
// the sql column. Record is non-blocking; on overflow the oldest queued entry
// is dropped to make room for the newest.
type History struct {
	db   *sql.DB
	ch   chan historyEntry
	stop chan struct{}
	done chan struct{}

	mu     sync.Mutex
	closed sync.Once

	dropped atomic.Uint64

	pausedMu sync.Mutex
	paused   bool
	pauseCh  chan struct{}
	pauseAck chan struct{}
	resumeCh chan struct{}
}

type historyEntry struct {
	executedAt   int64
	sql          string
	durMs        int64
	rowsAffected int64
	succeeded    bool
	connID       string
	source       string
	blocked      bool
}

// HistoryEvent records an attempt, not result data or output delivery. ExecutedAt
// is its completion time in Unix milliseconds; DurationMS includes DB cleanup.
// SQL literals and comments are retained. Parameters and result/error objects
// deliberately have no representation here.
type HistoryEvent struct {
	ExecutedAt   int64
	SQL          string
	DurationMS   int64
	ConnectionID string
	Source       string
	Outcome      string
}

func historyEntryForEvent(event HistoryEvent) (historyEntry, error) {
	if event.Source != "tui" && event.Source != "cli" {
		return historyEntry{}, errors.New("query: invalid history event source")
	}
	switch event.Outcome {
	case "succeeded", "failed", "blocked":
	default:
		return historyEntry{}, errors.New("query: invalid history event outcome")
	}
	return historyEntry{
		executedAt: event.ExecutedAt, sql: event.SQL, durMs: event.DurationMS,
		connID: event.ConnectionID, source: event.Source,
		succeeded: event.Outcome == "succeeded", blocked: event.Outcome == "blocked",
	}, nil
}

// RecordEntry validates and enqueues an attempt using Record's best-effort,
// nonblocking queue. A nil error means admission, not committed persistence.
// Source must be tui or cli; Outcome must be succeeded, failed or blocked.
func (h *History) RecordEntry(event HistoryEvent) error {
	entry, err := historyEntryForEvent(event)
	if err != nil {
		return err
	}
	h.enqueue(entry)
	return nil
}

// New opens (creating if needed) the SQLite history database at path, runs
// migrations, and starts the background writer goroutine. Startup is best-effort:
// lock contention must not prevent the TUI from opening without history.
func New(path string) (*History, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	db, err := openHistoryStore(ctx, path, false)
	if err != nil {
		return nil, err
	}

	h := &History{
		db:      db,
		ch:      make(chan historyEntry, historyChanCap),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		pauseCh: make(chan struct{}),
	}

	go h.writer()
	return h, nil
}

// Record enqueues a row for asynchronous insertion. It never blocks: if the
// queue is full, the oldest queued entry is evicted and the new one takes
// its slot. The dropped counter increments per eviction.
func (h *History) Record(stmt string, durMs int64, rowsAffected int64, succeeded bool, connID string) {
	entry := historyEntry{
		executedAt:   time.Now().UnixMilli(),
		sql:          stmt,
		durMs:        durMs,
		rowsAffected: rowsAffected,
		succeeded:    succeeded,
		connID:       connID,
		source:       "tui",
	}
	h.enqueue(entry)
}

func (h *History) enqueue(entry historyEntry) {
	select {
	case h.ch <- entry:
		return
	default:
	}

	h.dropOldestAndEnqueue(entry)
}

func (h *History) dropOldestAndEnqueue(entry historyEntry) {
	h.mu.Lock()
	defer h.mu.Unlock()

	select {
	case <-h.ch:
	default:
	}

	select {
	case h.ch <- entry:
	default:
		drops := h.dropped.Add(1)
		if drops%100 == 0 {
			slog.Warn("query/history: queue saturated; dropped entries", "total_dropped", drops)
			return
		}
		return
	}

	drops := h.dropped.Add(1)
	if drops%100 == 0 {
		slog.Warn("query/history: queue saturated; dropped entries", "total_dropped", drops)
	}
}

// Dropped reports the count of evicted entries since startup.
func (h *History) Dropped() uint64 { return h.dropped.Load() }

// Close stops the writer (after draining queued entries) and closes the
// underlying *sql.DB. Idempotent.
func (h *History) Close() error {
	var closeErr error
	h.closed.Do(func() {
		close(h.stop)
		h.resumeForTests()
		<-h.done
		closeErr = h.db.Close()
	})
	return closeErr
}

func (h *History) writer() {
	defer close(h.done)

	batch := make([]historyEntry, 0, historyBatchSize)
	ticker := time.NewTicker(historyFlushPeriod)
	defer ticker.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := h.flushBatch(batch); err != nil {
			slog.Warn("query/history: flush failed; dropping batch", "err", err, "n", len(batch))
		}
		batch = batch[:0]
	}

	for {
		h.pausedMu.Lock()
		paused, pauseCh, resumeCh := h.paused, h.pauseCh, h.resumeCh
		if paused && h.pauseAck != nil {
			close(h.pauseAck)
			h.pauseAck = nil
		}
		h.pausedMu.Unlock()
		if paused {
			select {
			case <-h.stop:
				h.drainAndFlush(&batch, flush)
				return
			case <-resumeCh:
			}
			continue
		}

		select {
		case <-pauseCh:
			continue
		case <-h.stop:
			h.drainAndFlush(&batch, flush)
			return
		case e := <-h.ch:
			batch = append(batch, e)
			if len(batch) >= historyBatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

func (h *History) drainAndFlush(batch *[]historyEntry, flush func()) {
	for {
		select {
		case e := <-h.ch:
			*batch = append(*batch, e)
			if len(*batch) >= historyBatchSize {
				flush()
			}
		default:
			flush()
			return
		}
	}
}

func (h *History) flushBatch(batch []historyEntry) error {
	tx, err := h.db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}

	var sb strings.Builder
	sb.WriteString(`INSERT INTO history(executed_at, sql, duration_ms, rows_affected, succeeded, connection_id, source, blocked) VALUES `)
	args := make([]any, 0, len(batch)*8)
	for i, e := range batch {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("(?,?,?,?,?,?,?,?)")
		succ := int64(0)
		if e.succeeded {
			succ = 1
		}
		blocked := int64(0)
		if e.blocked {
			blocked = 1
		}
		args = append(args, e.executedAt, e.sql, e.durMs, e.rowsAffected, succ, e.connID, e.source, blocked)
	}

	if _, err := tx.Exec(sb.String(), args...); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("insert: %w", err)
	}

	return tx.Commit()
}

func (h *History) isPaused() bool {
	h.pausedMu.Lock()
	defer h.pausedMu.Unlock()
	return h.paused
}

func (h *History) pauseWriter() {
	h.pausedMu.Lock()
	if !h.paused {
		h.paused = true
		h.resumeCh = make(chan struct{})
		h.pauseAck = make(chan struct{})
		close(h.pauseCh)
	}
	ack := h.pauseAck
	h.pausedMu.Unlock()
	if ack == nil {
		return
	}
	// Wake an idle select and wait until the writer has finished any
	// in-flight receive/flush and reached its parked state.
	select {
	case <-ack:
	case <-h.done:
	}
}

func (h *History) resumeWriter() {
	h.pausedMu.Lock()
	defer h.pausedMu.Unlock()
	if !h.paused {
		return
	}
	h.paused = false
	close(h.resumeCh)
	h.resumeCh = nil
	h.pauseCh = make(chan struct{})
}

func (h *History) resumeForTests() {
	h.resumeWriter()
}

// AsSessionRecorder returns an adapter that satisfies session.HistoryRecorder
// by forwarding every Record call to the underlying *History with the captured
// connID. *History itself has a 5-arg Record (canonical) and is bridged here.
func (h *History) AsSessionRecorder(connID string) session.HistoryRecorder {
	return &sessionRecorderAdapter{h: h, connID: connID}
}

type sessionRecorderAdapter struct {
	h      *History
	connID string
}

func (a *sessionRecorderAdapter) Record(stmt string, durMs int64, rowsAffected int64, succeeded bool) {
	a.h.Record(stmt, durMs, rowsAffected, succeeded, a.connID)
}

// HistoryRow is a single recorded statement read back from the history store.
type HistoryRow struct {
	ID           int64
	ExecutedAt   int64
	SQL          string
	DurationMS   int64
	Succeeded    bool
	ConnectionID string
	Source       string
	Outcome      string
}

// Recent returns up to limit rows from history, newest first (id DESC).
// limit <= 0 returns an empty (non-nil) slice and a nil error.
func (h *History) Recent(ctx context.Context, limit int) ([]HistoryRow, error) {
	if limit <= 0 {
		return []HistoryRow{}, nil
	}

	const q = `SELECT id, executed_at, sql, duration_ms, succeeded, connection_id, source, blocked
FROM history
ORDER BY id DESC
LIMIT ?`

	rows, err := h.db.QueryContext(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("query: history recent: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]HistoryRow, 0, limit)
	for rows.Next() {
		var (
			r             HistoryRow
			succ, blocked int64
		)
		if err := rows.Scan(&r.ID, &r.ExecutedAt, &r.SQL, &r.DurationMS, &succ, &r.ConnectionID, &r.Source, &blocked); err != nil {
			return nil, fmt.Errorf("query: history recent scan: %w", err)
		}
		r.Succeeded = succ != 0
		r.Outcome = "failed"
		if blocked != 0 {
			r.Outcome = "blocked"
		} else if r.Succeeded {
			r.Outcome = "succeeded"
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query: history recent rows: %w", err)
	}
	return out, nil
}

// SearchByPrefix returns up to limit distinct sql statements from history whose
// FTS5 token stream contains a token starting with prefix, ordered by most
// recent first. limit <= 0 or an empty/blank prefix returns an empty slice.
//
// The query uses quoted FTS5 prefix syntax (`MATCH '"sanitized"*'`); the prefix
// is sanitized to a single contiguous run of alphanumerics + underscore.
// Quoting makes operator words such as AND literal; the star stays outside
// the quotes to match longer tokens. Empty sanitization returns ([]string{}, nil).
//
// Blocked attempts remain manually recallable through Recent, but never supply
// completion candidates. De-duplication streams newest-first without GROUP BY.
func (h *History) SearchByPrefix(ctx context.Context, prefix string, limit int) ([]string, error) {
	if limit <= 0 {
		return []string{}, nil
	}
	clean := sanitizeFTSPrefix(prefix)
	if clean == "" {
		return []string{}, nil
	}

	// Cap the scan at a multiple of limit so a high-cardinality match set
	// (e.g. every row starts with SELECT) doesn't force us to read the
	// whole table just to dedupe.
	const scanMultiplier = 8
	scanCap := max(limit*scanMultiplier, 64)

	// Subquery returns matching rowids newest-first (rowid == history.id);
	// the outer SELECT does a covered PK lookup. Avoiding the JOIN+ORDER BY
	// keeps the FTS5 path lean enough to hit p99<50ms over 100k rows.
	const q = `SELECT sql FROM history
WHERE id IN (
    SELECT rowid FROM history_fts
    WHERE history_fts MATCH ?
      AND EXISTS (SELECT 1 FROM history AS candidate
                  WHERE candidate.id = history_fts.rowid AND candidate.blocked = 0)
    ORDER BY rowid DESC
    LIMIT ?
)
ORDER BY id DESC`

	rows, err := h.db.QueryContext(ctx, q, `"`+clean+`"*`, scanCap)
	if err != nil {
		return nil, fmt.Errorf("query: history search: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]string, 0, limit)
	seen := make(map[string]struct{}, limit)
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("query: history scan: %w", err)
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
		if len(out) >= limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query: history rows: %w", err)
	}
	return out, nil
}

// sanitizeFTSPrefix reduces prefix to a leading run of [A-Za-z0-9_] runes so
// the value can be safely encoded as `"value"*` in an FTS5 MATCH clause.
// Anything else (whitespace, quotes, operators) ends the prefix.
func sanitizeFTSPrefix(prefix string) string {
	var b strings.Builder
	for _, r := range prefix {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
			continue
		}
		break
	}
	return b.String()
}
