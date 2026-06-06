package logs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/afero"
)

// LogCloser is the published shutdown contract for the per-session logger.
// Implementations close the underlying file. CloseWithDeadline runs Close in
// a goroutine and force-closes the fd if Close exceeds d (AD-16).
type LogCloser interface {
	io.Closer
	CloseWithDeadline(d time.Duration) error
}

const (
	sessionFilePrefix = "dbsavvy-"
	sessionFileSuffix = ".log"
	sessionsSubdir    = "sessions"
)

// Open creates a per-session log file under opts.Dir/sessions/ and returns a
// *slog.Logger wired to a handler chain that:
//   - redacts secrets first (RedactingHandler),
//   - tees the redacted record to (a) a JSON file sink gated by opts.Categories
//     and (b) a stderr text sink gated to >= Warn.
//
// The returned LogCloser closes only the underlying file; close is idempotent.
// CloseWithDeadline force-closes the fd on timeout (AD-16).
func Open(opts Options) (*slog.Logger, LogCloser, error) {
	if opts.Dir == "" {
		return nil, nil, errors.New("logs: Options.Dir is empty (HOME/XDG_STATE_HOME may be unset)")
	}

	fs := opts.FS
	if fs == nil {
		fs = afero.NewOsFs()
	}
	clk := opts.Clock
	if clk == nil {
		clk = wallClock{}
	}
	retention := opts.RetentionCount
	if retention <= 0 {
		retention = 20
	}
	pid := opts.Pid
	if pid == 0 {
		pid = os.Getpid()
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}

	sessionsDir := filepath.Join(opts.Dir, sessionsSubdir)
	if err := fs.MkdirAll(sessionsDir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("logs: mkdir sessions dir: %w", err)
	}

	now := clk.Now()
	nano := now.UnixNano() % 1_000_000_000
	filename := fmt.Sprintf("%s%s-%d-%09d%s",
		sessionFilePrefix,
		now.Format("20060102-150405"),
		pid,
		nano,
		sessionFileSuffix,
	)
	target := filepath.Join(sessionsDir, filename)

	f, err := fs.OpenFile(target, os.O_APPEND|os.O_CREATE|os.O_WRONLY|platformNoFollow, 0o600)
	if err != nil {
		if isSymlinkLoopErr(err) {
			return nil, nil, fmt.Errorf("logs: refusing symlinked target: %w", err)
		}
		return nil, nil, fmt.Errorf("logs: open session file: %w", err)
	}
	// Best-effort enforce 0o600 on pre-existing files. afero MemMapFs returns
	// errors here that we ignore (it's a no-op for non-os backends).
	_ = fs.Chmod(target, 0o600)

	// Build the handler chain:
	//   RedactingHandler -> Tee{ CategoryFilter -> FileJSONHandler , StderrLevelGated }
	fileH := newFileJSONHandler(f)
	var fileBranch slog.Handler = fileH
	if cats := categorySet(opts.Categories); cats != nil {
		fileBranch = &categoryFilterHandler{next: fileH, categories: cats}
	}
	stderrH := newStderrLevelGated(stderr)
	tee := &teeHandler{branches: []slog.Handler{fileBranch, stderrH}}
	top := &redactingHandler{next: tee, redactor: opts.Redactor}
	logger := slog.New(top)

	// Retention sweep — post-open. Collect warnings, replay after startup marker.
	pruneWarnings := pruneSessions(fs, sessionsDir, retention)

	// Startup marker — first event written.
	Event(logger, "lifecycle", "startup_marker",
		slog.String("version", opts.BuildInfo.Version),
		slog.String("commit", opts.BuildInfo.Commit),
		slog.String("build_date", opts.BuildInfo.Date),
		slog.Int("pid", pid),
		slog.String("os", runtime.GOOS),
		slog.String("arch", runtime.GOARCH),
		slog.String("state_dir", opts.Dir),
		slog.String("sessions_dir", sessionsDir),
	)

	// Replay any pruning warnings as Warn-level entries.
	for _, w := range pruneWarnings {
		logger.LogAttrs(context.Background(), slog.LevelWarn, w,
			slog.String("cat", "lifecycle"),
			slog.String("evt", "retention_warn"),
		)
	}

	return logger, &sessionCloser{mu: &fileH.mu, f: f}, nil
}

// --- closer -----------------------------------------------------------------

type sessionCloser struct {
	mu   *sync.Mutex // shared with fileJSONHandler — coordinates Write vs Close
	f    afero.File
	once sync.Once
	err  error
}

func (c *sessionCloser) Close() error {
	c.once.Do(func() {
		c.mu.Lock()
		c.err = c.f.Close()
		c.mu.Unlock()
	})
	return c.err
}

// CloseWithDeadline runs Close in a goroutine. If it doesn't return within d,
// the underlying fd (when the file is backed by *os.File) is force-closed via
// syscall.Close and a single line is written to os.Stderr. Returns the Close
// error on success, or a deadline-exceeded error on timeout.
func (c *sessionCloser) CloseWithDeadline(d time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- c.Close() }()
	select {
	case err := <-done:
		return err
	case <-time.After(d):
		forceCloseFd(c.f)
		fmt.Fprintln(os.Stderr, "dbsavvy: log file close timed out; fd force-closed")
		return errors.New("logs: close deadline exceeded")
	}
}

// --- handlers ---------------------------------------------------------------

// NewRedactingHandler wraps next with a Redactor so every record is scrubbed
// before reaching downstream handlers. A nil redactor is treated as
// passthrough. Exposed for callers that build their own slog chain (e.g.
// entry_point's fallback / kill-switch paths in dbsavvy-962.2) and want the
// same redaction guarantee that Open() applies.
func NewRedactingHandler(next slog.Handler, r Redactor) slog.Handler {
	return &redactingHandler{next: next, redactor: r}
}

// redactingHandler wraps next with a Redactor. A nil redactor means passthrough.
type redactingHandler struct {
	next     slog.Handler
	redactor Redactor
}

func (h *redactingHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

func (h *redactingHandler) Handle(ctx context.Context, r slog.Record) error {
	if h.redactor != nil {
		// Defensive: if the redactor panics we still forward the (unredacted)
		// record so the application's logging call site doesn't crash. The
		// default redactor ALSO recovers internally; this is belt-and-braces.
		func() {
			defer func() { _ = recover() }()
			h.redactor.Redact(&r)
		}()
	}
	return h.next.Handle(ctx, r)
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &redactingHandler{next: h.next.WithAttrs(attrs), redactor: h.redactor}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{next: h.next.WithGroup(name), redactor: h.redactor}
}

// categoryFilterHandler drops records whose "cat" attr is not in the
// allowlist. A nil categories map means allow-all (defensive — Open() only
// installs this handler when categories is non-empty).
type categoryFilterHandler struct {
	next       slog.Handler
	categories map[string]struct{}
}

func (h *categoryFilterHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

func (h *categoryFilterHandler) Handle(ctx context.Context, r slog.Record) error {
	if h.categories == nil {
		return h.next.Handle(ctx, r)
	}
	var cat string
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "cat" {
			cat = a.Value.String()
			return false
		}
		return true
	})
	if _, ok := h.categories[cat]; !ok {
		return nil
	}
	return h.next.Handle(ctx, r)
}

func (h *categoryFilterHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &categoryFilterHandler{next: h.next.WithAttrs(attrs), categories: h.categories}
}

func (h *categoryFilterHandler) WithGroup(name string) slog.Handler {
	return &categoryFilterHandler{next: h.next.WithGroup(name), categories: h.categories}
}

// teeHandler fans out Handle to multiple branches. The record is Clone()d
// before being forwarded to each branch so branches can independently
// AddAttrs without corrupting siblings.
type teeHandler struct {
	branches []slog.Handler
}

func (h *teeHandler) Enabled(ctx context.Context, l slog.Level) bool {
	for _, b := range h.branches {
		if b.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (h *teeHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, b := range h.branches {
		_ = b.Handle(ctx, r.Clone())
	}
	return nil
}

func (h *teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	nb := make([]slog.Handler, len(h.branches))
	for i, b := range h.branches {
		nb[i] = b.WithAttrs(attrs)
	}
	return &teeHandler{branches: nb}
}

func (h *teeHandler) WithGroup(name string) slog.Handler {
	nb := make([]slog.Handler, len(h.branches))
	for i, b := range h.branches {
		nb[i] = b.WithGroup(name)
	}
	return &teeHandler{branches: nb}
}

// fileJSONHandler wraps slog.JSONHandler around an afero.File and serializes
// Write+Close through a sync.Mutex (shared with sessionCloser).
type fileJSONHandler struct {
	mu    sync.Mutex
	file  afero.File
	inner slog.Handler
}

func newFileJSONHandler(f afero.File) *fileJSONHandler {
	h := &fileJSONHandler{file: f}
	h.inner = slog.NewJSONHandler(&lockedWriter{h: h}, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})
	return h
}

// lockedWriter is a tiny io.Writer that acquires h.mu while writing to h.file.
// It is what slog.JSONHandler emits to, ensuring Write and Close serialize.
type lockedWriter struct{ h *fileJSONHandler }

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.h.mu.Lock()
	defer w.h.mu.Unlock()
	// Defensive: swallow Write errors (disk-full path tested in
	// TestOpen_DiskFullReturnsSilent). Returning an error from Write would
	// propagate to slog.Handle and eventually the caller; the existing
	// contract is to drop silently.
	n, _ := w.h.file.Write(p)
	if n != len(p) {
		// Pretend success so JSONHandler doesn't surface the error.
		return len(p), nil
	}
	return n, nil
}

func (h *fileJSONHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *fileJSONHandler) Handle(ctx context.Context, r slog.Record) error {
	return h.inner.Handle(ctx, r)
}

func (h *fileJSONHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h.inner.WithAttrs(attrs)
}

func (h *fileJSONHandler) WithGroup(name string) slog.Handler {
	return h.inner.WithGroup(name)
}

// stderrLevelGatedHandler emits only records with Level >= Warn, via an
// internal slog.TextHandler.
type stderrLevelGatedHandler struct {
	inner slog.Handler
}

func newStderrLevelGated(w io.Writer) *stderrLevelGatedHandler {
	return &stderrLevelGatedHandler{
		inner: slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelWarn}),
	}
}

func (h *stderrLevelGatedHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= slog.LevelWarn
}

func (h *stderrLevelGatedHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level < slog.LevelWarn {
		return nil
	}
	return h.inner.Handle(ctx, r)
}

func (h *stderrLevelGatedHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &stderrLevelGatedHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *stderrLevelGatedHandler) WithGroup(name string) slog.Handler {
	return &stderrLevelGatedHandler{inner: h.inner.WithGroup(name)}
}

// --- helpers ----------------------------------------------------------------

func categorySet(cats []string) map[string]struct{} {
	if len(cats) == 0 {
		return nil
	}
	m := make(map[string]struct{}, len(cats))
	for _, c := range cats {
		m[c] = struct{}{}
	}
	return m
}

func isSymlinkLoopErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ELOOP) {
		return true
	}
	return strings.Contains(err.Error(), "too many levels of symbolic links")
}

// pruneSessions removes oldest matching *.log files until count <= retention.
// Skips symlinks (returns warning strings the caller should log after the
// logger is constructed). Best-effort; errors are silently ignored.
func pruneSessions(fs afero.Fs, sessionsDir string, retention int) []string {
	var warnings []string

	entries, err := afero.ReadDir(fs, sessionsDir)
	if err != nil {
		return warnings
	}

	type fileEntry struct {
		path  string
		mtime int64
	}
	var files []fileEntry

	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, sessionFilePrefix) || !strings.HasSuffix(name, sessionFileSuffix) {
			continue
		}
		full := filepath.Join(sessionsDir, name)

		// Use Lstater if available to detect symlinks without following.
		var mode os.FileMode
		if lst, ok := fs.(afero.Lstater); ok {
			info, _, lerr := lst.LstatIfPossible(full)
			if lerr != nil {
				continue
			}
			mode = info.Mode()
			if mode&os.ModeSymlink != 0 {
				warnings = append(warnings, fmt.Sprintf("logs: skipped symlink in sessions dir: %s", name))
				continue
			}
			files = append(files, fileEntry{path: full, mtime: info.ModTime().UnixNano()})
			continue
		}
		// Fallback: plain Stat (follows symlinks; cannot detect them here).
		info, serr := fs.Stat(full)
		if serr != nil {
			continue
		}
		files = append(files, fileEntry{path: full, mtime: info.ModTime().UnixNano()})
	}

	if len(files) <= retention {
		return warnings
	}

	sort.Slice(files, func(i, j int) bool { return files[i].mtime < files[j].mtime })
	excess := len(files) - retention
	for i := range excess {
		_ = fs.Remove(files[i].path)
	}
	return warnings
}
