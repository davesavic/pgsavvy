package orchestrator_test

import (
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/gui/editor"
	"github.com/davesavic/pgsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

var errBufferSaveFixture = errors.New("synthetic buffer persistence failure")

type bufferSaveFS struct {
	afero.Fs
	started   chan struct{}
	release   chan struct{}
	committed chan string
	mkdirs    atomic.Int32
	fail      atomic.Int32 // 1: partial write; 2: rename
}

func (fs *bufferSaveFS) MkdirAll(path string, mode os.FileMode) error {
	if fs.mkdirs.Add(1) == 1 && fs.release != nil {
		close(fs.started)
		<-fs.release
	}
	return fs.Fs.MkdirAll(path, mode)
}

func (fs *bufferSaveFS) OpenFile(name string, flag int, mode os.FileMode) (afero.File, error) {
	f, err := fs.Fs.OpenFile(name, flag, mode)
	if err != nil {
		return nil, err
	}
	if fs.fail.Load() == 1 && flag&(os.O_WRONLY|os.O_RDWR) != 0 {
		return &failingBufferFile{File: f}, nil
	}
	return f, nil
}

type failingBufferFile struct{ afero.File }

func (f *failingBufferFile) Write(p []byte) (int, error) {
	n, _ := f.File.Write(p[:len(p)/2])
	return n, errBufferSaveFixture
}

func (f *failingBufferFile) WriteString(s string) (int, error) { return f.Write([]byte(s)) }

func (fs *bufferSaveFS) Rename(old, new string) error {
	if fs.fail.Load() == 2 {
		return errBufferSaveFixture
	}
	if err := fs.Fs.Rename(old, new); err != nil {
		return err
	}
	content, err := afero.ReadFile(fs.Fs, new)
	if err == nil && fs.committed != nil {
		fs.committed <- string(content)
	}
	return err
}

func buildBufferSaveGui(t *testing.T, gated bool) (*orchestrator.Gui, *queueingDriver, *bufferSaveFS, *editor.Buffer) {
	t.Helper()
	g, _, common := buildTestGuiWithCommon(t)
	fs := &bufferSaveFS{Fs: common.Fs, committed: make(chan string, 16)}
	if gated {
		fs.started, fs.release = make(chan struct{}), make(chan struct{})
	}
	common.Fs, common.StateDir = fs, "/buffer-state"
	driver := newQueueingDriver()
	if err := g.UseDriverForTest(driver); err != nil {
		t.Fatal(err)
	}
	driver.drainPending()
	buf := g.Registry().QueryEditor.Buffer()
	buf.ConnectionID, buf.UUID = "synthetic-connection", "deadbeef-1234-4567-89ab-cdef01234567"
	t.Cleanup(func() {
		g.WaitForWorkersForTest()
		driver.drainPending()
		_ = g.Close()
	})
	return g, driver, fs, buf
}

func setBufferSaveText(buf *editor.Buffer, text string) {
	buf.Lines = []editor.Line{{Runes: []rune(text)}}
	buf.Dirty = true
}

func flushBufferSave(t *testing.T, g *orchestrator.Gui) {
	t.Helper()
	if err := g.Registry().QueryEditor.HandleFocusLost(types.OnFocusLostOpts{}); err != nil {
		t.Fatal(err)
	}
}

func assertBufferSaveText(t *testing.T, fs afero.Fs, buf *editor.Buffer, want string) {
	t.Helper()
	loaded, err := editor.LoadBuffer(fs, "/buffer-state", buf.ConnectionID, buf.UUID)
	if err != nil || loaded.String() != want {
		t.Fatalf("persisted text = %q, error = %v; want %q", loaded.String(), err, want)
	}
}

func TestQueryEditorSavesInDispatchOrder(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		name := "focus-loss"
		if shutdown {
			name = "shutdown"
		}
		t.Run(name, func(t *testing.T) {
			g, driver, fs, buf := buildBufferSaveGui(t, true)
			release := sync.OnceFunc(func() { close(fs.release) })
			defer release()
			setBufferSaveText(buf, "SELECT older")
			flushBufferSave(t, g)
			select {
			case <-fs.started:
			case <-time.After(time.Second):
				t.Fatal("older save never reached filesystem gate")
			}
			if !buf.Dirty {
				t.Error("save dispatch cleared Dirty before persistence")
			}
			setBufferSaveText(buf, "SELECT newest")
			closed := make(chan error, 1)
			if shutdown {
				go func() { closed <- g.Close() }()
			} else {
				flushBufferSave(t, g)
			}
			// The old implementation completes the newer write while the older
			// one is parked. An ordered writer cannot enter the filesystem yet.
			select {
			case text := <-fs.committed:
				t.Errorf("save %q overtook the gated older snapshot", text)
			case <-time.After(100 * time.Millisecond):
			}
			release()
			if shutdown {
				select {
				case err := <-closed:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("Close did not drain saves")
				}
			} else {
				g.WaitForWorkersForTest()
				driver.drainPending()
			}
			assertBufferSaveText(t, fs, buf, "SELECT newest")
			if buf.Dirty {
				t.Error("successful latest persistence did not clear Dirty")
			}
		})
	}
}

func TestQueryEditorSaveFailureRetainsDirtyAndCanRetry(t *testing.T) {
	for _, fail := range []int32{1, 2} {
		name := "write"
		if fail == 2 {
			name = "rename"
		}
		t.Run(name, func(t *testing.T) {
			g, driver, fs, buf := buildBufferSaveGui(t, false)
			if err := editor.SaveBufferContent(fs, "/buffer-state", buf.ConnectionID, buf.UUID, "SELECT baseline"); err != nil {
				t.Fatal(err)
			}
			fs.fail.Store(fail)
			setBufferSaveText(buf, "SELECT retry")
			flushBufferSave(t, g)
			g.WaitForWorkersForTest()
			driver.drainPending()
			if !buf.Dirty {
				t.Error("failed persistence cleared Dirty")
			}
			assertBufferSaveText(t, fs, buf, "SELECT baseline")
			if err := afero.Walk(fs, "/buffer-state", func(path string, info os.FileInfo, err error) error {
				if err == nil && !info.IsDir() && strings.HasSuffix(path, ".tmp") {
					t.Errorf("failed save left temporary file %s", path)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			fs.fail.Store(0)
			flushBufferSave(t, g)
			g.WaitForWorkersForTest()
			driver.drainPending()
			assertBufferSaveText(t, fs, buf, "SELECT retry")
			if buf.Dirty {
				t.Fatal("successful retry did not clear Dirty")
			}
		})
	}
}

func TestQueryEditorCloseSaveFailureRetainsDirty(t *testing.T) {
	g, _, fs, buf := buildBufferSaveGui(t, false)
	fs.fail.Store(2)
	setBufferSaveText(buf, "SELECT unsaved")
	if err := g.Close(); !errors.Is(err, errBufferSaveFixture) {
		t.Errorf("Close error = %v, want persistence error", err)
	}
	if !buf.Dirty {
		t.Error("failed shutdown save cleared Dirty")
	}
}

func TestQueryEditorInvalidSaveDestinationRetainsDirty(t *testing.T) {
	g, driver, _, buf := buildBufferSaveGui(t, false)
	buf.UUID = "nonempty-but-invalid"
	setBufferSaveText(buf, "SELECT unsaved")
	flushBufferSave(t, g)
	g.WaitForWorkersForTest()
	driver.drainPending()
	if !buf.Dirty {
		t.Error("skipped persistence acknowledged as success")
	}
	if err := g.Close(); err == nil {
		t.Error("invalid final-save destination did not return an error")
	}
	if !buf.Dirty {
		t.Error("skipped final save cleared Dirty")
	}
}

func TestQueryEditorCloseSaveDrainTimeoutRetainsDirty(t *testing.T) {
	g, _, fs, buf := buildBufferSaveGui(t, true)
	release := sync.OnceFunc(func() { close(fs.release) })
	defer release()
	setBufferSaveText(buf, "SELECT older")
	flushBufferSave(t, g)
	<-fs.started
	setBufferSaveText(buf, "SELECT unsaved newest")
	closed := make(chan error, 1)
	go func() { closed <- g.Close() }()
	select {
	case err := <-closed:
		if err == nil || !strings.Contains(err.Error(), "drain") {
			t.Errorf("Close error = %v, want save drain timeout", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("Close exceeded the bounded worker drain")
	}
	if !buf.Dirty {
		t.Error("undrained shutdown marked newest buffer saved")
	}
	select {
	case text := <-fs.committed:
		t.Errorf("shutdown wrote %q while an older writer could still overwrite it", text)
	default:
	}
	release()
	g.WaitForWorkersForTest()
}
