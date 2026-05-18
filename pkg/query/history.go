package query

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/davesavic/dbsavvy/pkg/session"

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
	resumeCh chan struct{}
}

type historyEntry struct {
	executedAt   int64
	sql          string
	durMs        int64
	rowsAffected int64
	succeeded    bool
	connID       string
}

// New opens (creating if needed) the SQLite history database at path, runs
// migrations, and starts the background writer goroutine.
func New(path string) (*History, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("query: mkdir history dir: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("query: open sqlite: %w", err)
	}

	db.SetMaxOpenConns(1)

	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL;`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("query: pragma: %w", err)
	}

	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	h := &History{
		db:   db,
		ch:   make(chan historyEntry, historyChanCap),
		stop: make(chan struct{}),
		done: make(chan struct{}),
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
	}

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
		if h.isPaused() {
			select {
			case <-h.stop:
				h.drainAndFlush(&batch, flush)
				return
			case <-h.waitResume():
			}
			continue
		}

		select {
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
	sb.WriteString(`INSERT INTO history(executed_at, sql, duration_ms, rows_affected, succeeded, connection_id) VALUES `)
	args := make([]any, 0, len(batch)*6)
	for i, e := range batch {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("(?,?,?,?,?,?)")
		succ := int64(0)
		if e.succeeded {
			succ = 1
		}
		args = append(args, e.executedAt, e.sql, e.durMs, e.rowsAffected, succ, e.connID)
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

func (h *History) waitResume() <-chan struct{} {
	h.pausedMu.Lock()
	defer h.pausedMu.Unlock()
	if !h.paused {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return h.resumeCh
}

func (h *History) pauseWriter() {
	h.pausedMu.Lock()
	defer h.pausedMu.Unlock()
	if h.paused {
		return
	}
	h.paused = true
	h.resumeCh = make(chan struct{})
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
}

func (h *History) resumeForTests() {
	h.pausedMu.Lock()
	defer h.pausedMu.Unlock()
	if h.paused {
		h.paused = false
		close(h.resumeCh)
		h.resumeCh = nil
	}
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
