package logs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
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
// logrus logger configured to route DEBUG+ to that file (gated by
// opts.Categories, if non-empty) and WARN+ to opts.Stderr.
//
// The returned LogCloser closes only the underlying file; close is idempotent.
// CloseWithDeadline force-closes the fd on timeout (AD-16).
func Open(opts Options) (*logrus.Logger, LogCloser, error) {
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

	// Build logger.
	logger := logrus.New()
	logger.SetLevel(logrus.DebugLevel)
	logger.SetOutput(io.Discard) // hooks do all routing
	logger.SetFormatter(&logrus.JSONFormatter{TimestampFormat: "2006-01-02T15:04:05.000000000Z07:00"})

	// Redactor first so subsequent hooks see redacted entries.
	if opts.Redactor != nil {
		logger.AddHook(opts.Redactor)
	}

	fh := &fileHook{
		file:       f,
		formatter:  &logrus.JSONFormatter{TimestampFormat: "2006-01-02T15:04:05.000000000Z07:00"},
		categories: categorySet(opts.Categories),
	}
	logger.AddHook(fh)

	sh := &stderrHook{
		w:         stderr,
		formatter: &logrus.TextFormatter{DisableColors: true, FullTimestamp: true},
	}
	logger.AddHook(sh)

	// Retention sweep — post-open. Collect warnings, replay after startup marker.
	pruneWarnings := pruneSessions(fs, sessionsDir, retention)

	// Startup marker — first event written.
	Event(logger, "lifecycle", "startup_marker", logrus.Fields{
		"version":      opts.BuildInfo.Version,
		"commit":       opts.BuildInfo.Commit,
		"build_date":   opts.BuildInfo.Date,
		"pid":          pid,
		"os":           runtime.GOOS,
		"arch":         runtime.GOARCH,
		"state_dir":    opts.Dir,
		"sessions_dir": sessionsDir,
	})

	// Replay any pruning warnings.
	for _, w := range pruneWarnings {
		logger.WithFields(logrus.Fields{"cat": "lifecycle", "evt": "retention_warn"}).Warn(w)
	}

	return logger, &sessionCloser{f: f}, nil
}

// --- closer -----------------------------------------------------------------

type sessionCloser struct {
	f    afero.File
	once sync.Once
	err  error
}

func (c *sessionCloser) Close() error {
	c.once.Do(func() {
		c.err = c.f.Close()
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

// --- hooks ------------------------------------------------------------------

type fileHook struct {
	mu         sync.Mutex
	file       afero.File
	formatter  logrus.Formatter
	categories map[string]struct{}
}

func (h *fileHook) Levels() []logrus.Level { return logrus.AllLevels }

func (h *fileHook) Fire(entry *logrus.Entry) error {
	if len(h.categories) > 0 {
		raw, ok := entry.Data["cat"]
		if !ok {
			return nil
		}
		catStr, _ := raw.(string)
		if _, found := h.categories[catStr]; !found {
			return nil
		}
	}
	b, err := h.formatter.Format(entry)
	if err != nil {
		return nil // swallow formatter errors
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	_, _ = h.file.Write(b) // swallow write errors (disk-full path)
	return nil
}

type stderrHook struct {
	mu        sync.Mutex
	w         io.Writer
	formatter logrus.Formatter
}

func (h *stderrHook) Levels() []logrus.Level {
	return []logrus.Level{
		logrus.PanicLevel,
		logrus.FatalLevel,
		logrus.ErrorLevel,
		logrus.WarnLevel,
	}
}

func (h *stderrHook) Fire(entry *logrus.Entry) error {
	b, err := h.formatter.Format(entry)
	if err != nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	_, _ = h.w.Write(b)
	return nil
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
	for i := 0; i < excess; i++ {
		_ = fs.Remove(files[i].path)
	}
	return warnings
}
