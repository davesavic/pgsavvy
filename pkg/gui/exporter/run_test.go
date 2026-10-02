package exporter

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/models"
)

type fakeRowSource struct {
	cols []models.ColumnMeta
	rows []models.Row
}

func (f *fakeRowSource) Cols() []models.ColumnMeta { return f.cols }

func (f *fakeRowSource) Iterate(fn func(models.Row) error) error {
	for _, r := range f.rows {
		if err := fn(r); err != nil {
			return err
		}
	}
	return nil
}

// slowRowSource sleeps between emissions so context cancellation has a
// reliable window to take effect.
type slowRowSource struct {
	cols  []models.ColumnMeta
	rows  []models.Row
	sleep time.Duration
}

func (s *slowRowSource) Cols() []models.ColumnMeta { return s.cols }

func (s *slowRowSource) Iterate(fn func(models.Row) error) error {
	for _, r := range s.rows {
		if err := fn(r); err != nil {
			return err
		}
		time.Sleep(s.sleep)
	}
	return nil
}

func TestRun_CSVToFile_HappyPath(t *testing.T) {
	dir := t.TempDir()
	src := &fakeRowSource{
		cols: []models.ColumnMeta{{Name: "id"}, {Name: "name"}},
		rows: []models.Row{
			{Values: []any{1, "alice"}},
			{Values: []any{2, "bob"}},
			{Values: []any{3, "carol"}},
		},
	}
	dest := NewFileDest(dir, "out.csv")
	desc, err := Run(context.Background(), NewCSV(), dest, src, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if desc != filepath.Join(dir, "out.csv") {
		t.Fatalf("descriptor=%q", desc)
	}
	b, err := os.ReadFile(filepath.Join(dir, "out.csv"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	want := "id,name\r\n1,alice\r\n2,bob\r\n3,carol\r\n"
	if string(b) != want {
		t.Fatalf("contents=%q want=%q", string(b), want)
	}
}

func TestRun_ProgressCalled(t *testing.T) {
	const n = 10001
	rows := make([]models.Row, n)
	for i := range rows {
		rows[i] = models.Row{Values: []any{i}}
	}
	src := &fakeRowSource{
		cols: []models.ColumnMeta{{Name: "id"}},
		rows: rows,
	}
	dir := t.TempDir()
	dest := NewFileDest(dir, "out.csv")
	var calls atomic.Int64
	progress := func(rowsWritten int64) {
		calls.Add(1)
	}
	if _, err := Run(context.Background(), NewCSV(), dest, src, progress); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := calls.Load(); got < 2 {
		t.Fatalf("progress calls=%d want>=2", got)
	}
}

func TestRun_CancellationAbortsAndCleansUp(t *testing.T) {
	rows := make([]models.Row, 100)
	for i := range rows {
		rows[i] = models.Row{Values: []any{i}}
	}
	src := &slowRowSource{
		cols:  []models.ColumnMeta{{Name: "id"}},
		rows:  rows,
		sleep: 5 * time.Millisecond,
	}
	dir := t.TempDir()
	dest := NewFileDest(dir, "out.csv")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(25 * time.Millisecond)
		cancel()
	}()
	_, err := Run(ctx, NewCSV(), dest, src, nil)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v want context.Canceled", err)
	}
	final := filepath.Join(dir, "out.csv")
	if _, err := os.Stat(final); !os.IsNotExist(err) {
		t.Fatalf("final exists after cancel: err=%v", err)
	}
	if _, err := os.Stat(final + ".partial"); !os.IsNotExist(err) {
		t.Fatalf(".partial exists after cancel: err=%v", err)
	}
}

// failingDest returns an error from Open.
type failingDest struct{}

func (failingDest) Open() (io.WriteCloser, string, error) {
	return nil, "", errors.New("open failed")
}

func TestRun_OpenFailure_NoSideEffects(t *testing.T) {
	src := &fakeRowSource{
		cols: []models.ColumnMeta{{Name: "id"}},
		rows: []models.Row{{Values: []any{1}}},
	}
	_, err := Run(context.Background(), NewCSV(), failingDest{}, src, nil)
	if err == nil {
		t.Fatal("expected Open failure error")
	}
	if !strings.Contains(err.Error(), "open failed") {
		t.Fatalf("err=%v want contains 'open failed'", err)
	}
}

// footerErrFormat returns an error from Footer to exercise the failure path
// that should still trigger destination cleanup.
type footerErrFormat struct{}

func (footerErrFormat) Name() string      { return "fail" }
func (footerErrFormat) Ext() string       { return "fail" }
func (footerErrFormat) IsStreaming() bool { return true }
func (footerErrFormat) Header(_ []models.ColumnMeta, w io.Writer) error {
	_, err := w.Write([]byte("header\n"))
	return err
}

func (footerErrFormat) Row(_ models.Row, w io.Writer) error {
	_, err := w.Write([]byte("row\n"))
	return err
}
func (footerErrFormat) Footer(_ io.Writer) error { return errors.New("footer boom") }

func TestRun_FooterErrorAbortsFile(t *testing.T) {
	dir := t.TempDir()
	src := &fakeRowSource{
		cols: []models.ColumnMeta{{Name: "id"}},
		rows: []models.Row{{Values: []any{1}}},
	}
	dest := NewFileDest(dir, "out.fail")
	_, err := Run(context.Background(), footerErrFormat{}, dest, src, nil)
	if err == nil {
		t.Fatal("expected footer error")
	}
	final := filepath.Join(dir, "out.fail")
	if _, err := os.Stat(final); !os.IsNotExist(err) {
		t.Fatalf("final exists after footer error: err=%v", err)
	}
	if _, err := os.Stat(final + ".partial"); !os.IsNotExist(err) {
		t.Fatalf(".partial exists after footer error: err=%v", err)
	}
}

type reviewClipboardWriter struct {
	last  string
	calls int
	err   error
}

func (c *reviewClipboardWriter) Write(text string) error {
	c.calls++
	if c.calls == 1 && c.err != nil {
		return c.err
	}
	c.last = text
	return nil
}

type reviewClipboardFormat struct {
	Format
	afterHeader, afterRow, afterFooter func() error
}

func (f reviewClipboardFormat) Header(cols []models.ColumnMeta, w io.Writer) error {
	if err := f.Format.Header(cols, w); err != nil {
		return err
	}
	if f.afterHeader != nil {
		return f.afterHeader()
	}
	return nil
}

func (f reviewClipboardFormat) Row(row models.Row, w io.Writer) error {
	if err := f.Format.Row(row, w); err != nil {
		return err
	}
	if f.afterRow != nil {
		return f.afterRow()
	}
	return nil
}

func (f reviewClipboardFormat) Footer(w io.Writer) error {
	if err := f.Format.Footer(w); err != nil {
		return err
	}
	if f.afterFooter != nil {
		return f.afterFooter()
	}
	return nil
}

type reviewClipboardSource struct {
	fakeRowSource
	after func() error
}

func (s reviewClipboardSource) Iterate(fn func(models.Row) error) error {
	if err := s.fakeRowSource.Iterate(fn); err != nil {
		return err
	}
	if s.after != nil {
		return s.after()
	}
	return nil
}

func TestReview_clipboard(t *testing.T) {
	for _, scenario := range []string{"success", "empty_success", "header", "row", "footer", "source", "cap", "publish", "pre_canceled_empty", "mid_row_cancel", "source_end_cancel", "empty_source_cancel", "footer_cancel", "progress_cancel"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			failure := errors.New("synthetic export failure")
			fail := func() error { return failure }
			cancelNow := func() error { cancel(); return nil }
			cb := &reviewClipboardWriter{last: "previous clipboard"}
			format := reviewClipboardFormat{Format: NewCSV()}
			src := reviewClipboardSource{fakeRowSource: fakeRowSource{
				cols: []models.ColumnMeta{{Name: "id"}},
				rows: []models.Row{{Values: []any{1}}, {Values: []any{2}}},
			}}
			capBytes := int64(1024)
			var progress ProgressFn
			switch scenario {
			case "header":
				format.afterHeader = fail
			case "row":
				format.afterRow = fail
			case "footer":
				format.afterFooter = fail
			case "source":
				src.after = fail
			case "cap":
				capBytes = 5 // Header fits; the first CSV row exceeds the cap.
			case "publish":
				cb.err = failure
			case "pre_canceled_empty":
				src.rows = nil
				cancel()
			case "mid_row_cancel":
				format.afterRow = cancelNow
			case "source_end_cancel":
				src.after = cancelNow
			case "empty_source_cancel":
				src.rows, src.after = nil, cancelNow
			case "footer_cancel":
				format.afterFooter = cancelNow
			case "progress_cancel":
				src.rows = src.rows[:1]
				progress = func(int64) { cancel() }
			case "empty_success":
				src.rows = nil
			}
			desc, err := Run(ctx, format, NewClipboardDest(cb, capBytes), &src, progress)
			if scenario == "success" || scenario == "empty_success" {
				want := "id\r\n1\r\n2\r\n"
				if scenario == "empty_success" {
					want = "id\r\n"
				}
				if err != nil || desc != "clipboard" || cb.calls != 1 || cb.last != want {
					t.Fatalf("desc=%q err=%v calls=%d text=%q, want one complete publication", desc, err, cb.calls, cb.last)
				}
				return
			}
			if err == nil || desc != "" {
				t.Errorf("desc=%q err=%v, want failed export", desc, err)
			}
			if strings.Contains(scenario, "cancel") {
				if !errors.Is(err, context.Canceled) {
					t.Errorf("err=%v, want context.Canceled", err)
				}
			} else if scenario != "cap" && !errors.Is(err, failure) {
				t.Errorf("err=%v, want original failure", err)
			}
			wantCalls := 0
			if scenario == "publish" {
				wantCalls = 1
			}
			if cb.calls != wantCalls || cb.last != "previous clipboard" {
				t.Errorf("calls=%d text=%q, want %d calls and unchanged clipboard", cb.calls, cb.last, wantCalls)
			}
		})
	}
}

type reviewClipboardDest struct{ io.WriteCloser }

func (d reviewClipboardDest) Open() (io.WriteCloser, string, error) {
	return d.WriteCloser, "stdout", nil
}

func TestReview_clipboard_OtherDestinations(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	format := reviewClipboardFormat{Format: NewCSV(), afterFooter: func() error { cancel(); return nil }}
	src := &fakeRowSource{cols: []models.ColumnMeta{{Name: "id"}}, rows: []models.Row{{Values: []any{1}}}}
	dir := t.TempDir()
	final := filepath.Join(dir, "out.csv")
	if err := os.WriteFile(final, []byte("previous file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if desc, err := Run(ctx, format, NewFileDest(dir, "out.csv"), src, nil); !errors.Is(err, context.Canceled) || desc != "" {
		t.Errorf("late-canceled file export desc=%q err=%v", desc, err)
	}
	contents, err := os.ReadFile(final)
	if err != nil || string(contents) != "previous file" {
		t.Errorf("late cancellation replaced original file: %q err=%v", contents, err)
	}
	if _, err := os.Stat(final + ".partial"); !os.IsNotExist(err) {
		t.Errorf("partial file remains: %v", err)
	}
	var stdout bytes.Buffer
	if _, err := Run(context.Background(), footerErrFormat{}, reviewClipboardDest{nopCloser{&stdout}}, src, nil); err == nil {
		t.Fatal("expected stdout format failure")
	}
	if _, err := stdout.WriteString("still open"); err != nil || stdout.String() != "header\nrow\nstill open" {
		t.Errorf("stdout failure contract changed: %q err=%v", stdout.String(), err)
	}
	wc, _, err := NewStdoutDest().Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := wc.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stdout.Stat(); err != nil {
		t.Fatalf("stdout closed by destination: %v", err)
	}
}
