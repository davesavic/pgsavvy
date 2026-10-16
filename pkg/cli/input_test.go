//go:build linux

package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
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
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dir, fifo, "/dev/null"} {
		if _, err := ReadSQL(context.Background(), path, nil); err == nil {
			t.Fatal("nonregular SQL path accepted")
		}
	}
	file := filepath.Join(dir, "query")
	if err := os.WriteFile(file, []byte("SELECT 1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if sql, err := ReadSQL(context.Background(), file, nil); err != nil || sql != "SELECT 1" {
		t.Fatal("regular SQL file failed")
	}
}

func TestMachineQuery_Cancel_Input(t *testing.T) {
	read, write, err := os.Pipe()
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
