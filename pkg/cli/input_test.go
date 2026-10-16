//go:build linux || darwin || windows

package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMachineQuery_Parameters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "params.json")
	for _, tc := range []struct {
		input string
		want  [][]byte
	}{
		{`[]`, [][]byte{}}, {`[null,"",true,false,9007199254740993,1.2300e+25,"{\"x\":null}"]`, [][]byte{nil, {}, []byte("true"), []byte("false"), []byte("9007199254740993"), []byte("1.2300e+25"), []byte(`{"x":null}`)}},
	} {
		if err := os.WriteFile(path, []byte(tc.input), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := ReadParameters(context.Background(), path)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("parameters: %q %v", got, err)
		}
	}
	for _, input := range []string{"", "null", "{}", "[[]]", "[{}]", "[true] false", "[1,]", "[NaN]", "[" + strings.Repeat("0,", 256) + "0]", string([]byte{'[', '"', 0xff, '"', ']'}), strings.Repeat(" ", MaxInputBytes+1)} {
		if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadParameters(context.Background(), path); err == nil {
			t.Fatalf("invalid parameter input accepted: bytes%d", len(input))
		}
	}
	if _, err := ReadParameters(context.Background(), "-"); err == nil {
		t.Fatal("parameter stdin accepted")
	}
	input := "[" + strings.Repeat("0,", 255) + "0]"
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	if values, err := ReadParameters(context.Background(), path); err != nil || len(values) != 256 {
		t.Fatal("maximum parameter count rejected")
	}
}

func TestMachineQuery_Bounds_Input(t *testing.T) {
	for _, size := range []int{MaxInputBytes, MaxInputBytes + 1} {
		sql := "SELECT 1 --" + strings.Repeat("x", size-len("SELECT 1 --"))
		got, err := ReadSQL(context.Background(), "-", io.NopCloser(strings.NewReader(sql)))
		if size == MaxInputBytes {
			if err != nil || got != sql {
				t.Fatal("inclusive SQL budget rejected")
			}
		} else if err == nil {
			t.Fatal("oversized SQL accepted")
		}
	}
	for _, text := range []string{"", " \n\t", string([]byte{0xff})} {
		if _, err := ReadSQL(context.Background(), "-", io.NopCloser(strings.NewReader(text))); err == nil {
			t.Fatal("empty/invalid SQL accepted")
		}
	}
	dir := t.TempDir()
	if _, err := ReadSQL(context.Background(), dir, nil); safeError(err).Code != InvalidInput {
		t.Fatal("SQL directory did not return invalid input")
	}
	file := filepath.Join(dir, "query")
	if err := os.WriteFile(file, []byte("SELECT 1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if sql, err := ReadSQL(context.Background(), file, nil); err != nil || sql != "SELECT 1" {
		t.Fatal("regular SQL file failed")
	}
}

func TestMachineInput_FileBoundsAndErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "query λ with spaces.sql")
	for _, size := range []int{MaxInputBytes, MaxInputBytes + 1} {
		sql := "SELECT 'λ'; --" + strings.Repeat("x", size-len("SELECT 'λ'; --"))
		if err := os.WriteFile(path, []byte(sql), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := ReadSQL(context.Background(), path, nil)
		if size == MaxInputBytes {
			if err != nil || got != sql {
				t.Fatal("inclusive file SQL budget or original bytes changed")
			}
		} else if safeError(err).Code != InvalidInput {
			t.Fatal("oversized SQL file did not return invalid input")
		}
		parameters := "[\"" + strings.Repeat("x", size-4) + "\"]"
		if err := os.WriteFile(path, []byte(parameters), 0o600); err != nil {
			t.Fatal(err)
		}
		values, err := ReadParameters(context.Background(), path)
		if size == MaxInputBytes {
			if err != nil || len(values) != 1 || len(values[0]) != size-4 {
				t.Fatal("inclusive parameter file budget rejected")
			}
		} else if safeError(err).Code != InvalidInput {
			t.Fatal("oversized parameter file did not return invalid input")
		}
	}
	for _, input := range []string{"", " \n\t", string([]byte{0xff})} {
		if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadSQL(context.Background(), path, nil); safeError(err).Code != InvalidInput {
			t.Fatal("malformed SQL file did not return invalid input")
		}
	}
	if _, err := ReadSQL(context.Background(), path+"-missing-SECRET", nil); safeError(err).Code != ResourceFailure || strings.Contains(err.Error(), "SECRET") {
		t.Fatal("missing input classification or privacy changed")
	}
	if _, err := ReadParameters(context.Background(), "-"); safeError(err).Code != InvalidInput {
		t.Fatal("parameter stdin classification changed")
	}
	for _, text := range []string{"null", "{}", "[[]]", "[{}]", "[] []"} {
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadParameters(context.Background(), path); safeError(err).Code != InvalidInput {
			t.Fatal("malformed parameters classification changed")
		}
	}
}

type machineFailingInput struct{ closed atomic.Bool }

func (*machineFailingInput) Read([]byte) (int, error) {
	return 0, errors.New("SECRET native read error")
}
func (r *machineFailingInput) Close() error { r.closed.Store(true); return nil }

func TestMachineInput_ReadFailure(t *testing.T) {
	reader := &machineFailingInput{}
	_, err := ReadSQL(context.Background(), "-", reader)
	if safeError(err).Code != ResourceFailure || strings.Contains(err.Error(), "SECRET") || reader.closed.Load() {
		t.Fatal("read failure classification/privacy or borrowed stream ownership changed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadSQL(ctx, "-", reader); !errors.Is(err, context.Canceled) || reader.closed.Load() {
		t.Fatal("pre-canceled input touched borrowed resources")
	}
}

func TestMachineInput_ConcurrentCancellation(t *testing.T) {
	var workers sync.WaitGroup
	for range 20 {
		workers.Go(func() {
			read, write, err := machineInputPipe()
			if err != nil {
				t.Error(err)
				return
			}
			defer func() { _ = read.Close(); _ = write.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			start := time.Now()
			if _, err := ReadSQL(ctx, "-", read); !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
				t.Error("concurrent silent input did not cancel and join")
			}
			cancel()
			cancel()
			if got, err := ReadSQL(context.Background(), "-", io.NopCloser(strings.NewReader("SELECT 1"))); err != nil || got != "SELECT 1" {
				t.Error("cancellation affected independent invocation")
			}
		})
	}
	workers.Wait()
}

type machineJoiningInput struct {
	readStarted, readReleased, closeStarted, closeReleased chan struct{}
}

func (r *machineJoiningInput) Read([]byte) (int, error) {
	close(r.readStarted)
	<-r.readReleased
	return 0, io.ErrClosedPipe
}

func (r *machineJoiningInput) Close() error {
	close(r.readReleased)
	close(r.closeStarted)
	<-r.closeReleased
	return nil
}

func TestMachineInput_CancellationJoinsWatcher(t *testing.T) {
	reader := &machineJoiningInput{make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := sync.OnceFunc(func() { close(reader.closeReleased) })
	defer release()
	done := make(chan error, 1)
	go func() { _, err := ReadSQL(ctx, "-", reader); done <- err }()
	select {
	case <-reader.readStarted:
	case <-time.After(time.Second):
		t.Fatal("input read did not start")
	}
	cancel()
	select {
	case <-reader.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not close input")
	}
	select {
	case <-done:
		t.Fatal("input returned before its cancellation watcher joined")
	case <-time.After(20 * time.Millisecond):
	}
	release()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("cancellation classification changed")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation watcher did not join")
	}
}

func TestMachineQuery_Cancel_Input(t *testing.T) {
	read, write, err := machineInputPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = read.Close(); _ = write.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := ReadSQL(ctx, "-", read); !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("silent stdin cancellation: %v", err)
	}
}
