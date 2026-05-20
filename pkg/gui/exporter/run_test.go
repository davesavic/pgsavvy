package exporter

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/models"
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
