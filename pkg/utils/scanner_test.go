package utils

import (
	"bufio"
	"bytes"
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/constants"
)

func TestScanLines_BasicLines(t *testing.T) {
	in := bytes.NewBufferString("alpha\nbeta\ngamma\n")
	s := bufio.NewScanner(in)
	s.Split(ScanLinesAndTruncateWhenLongerThanBuffer)
	var got []string
	for s.Scan() {
		got = append(got, s.Text())
	}
	if err := s.Err(); err != nil {
		t.Fatalf("scanner err: %v", err)
	}
	want := []string{"alpha", "beta", "gamma"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestScanLines_EmptyInput(t *testing.T) {
	s := bufio.NewScanner(bytes.NewBufferString(""))
	s.Split(ScanLinesAndTruncateWhenLongerThanBuffer)
	count := 0
	for s.Scan() {
		count++
	}
	if err := s.Err(); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if count != 0 {
		t.Fatalf("got %d lines, want 0", count)
	}
}

func TestScanLines_TruncatesOversizeLine(t *testing.T) {
	huge := strings.Repeat("x", 2*constants.MaxLineLength) + "\n"
	s := bufio.NewScanner(strings.NewReader(huge))
	// bufio default max is 64 KiB; bump to a hair over the cap so our split
	// func can deliver a truncated 1 MiB token without ErrTooLong.
	s.Buffer(make([]byte, 0, 64*1024), constants.MaxLineLength+1024)
	s.Split(ScanLinesAndTruncateWhenLongerThanBuffer)
	if !s.Scan() {
		t.Fatalf("expected at least one token, err=%v", s.Err())
	}
	tok := s.Bytes()
	if len(tok) != constants.MaxLineLength {
		t.Fatalf("token len = %d, want %d", len(tok), constants.MaxLineLength)
	}
	if err := s.Err(); err != nil {
		t.Fatalf("scanner err: %v", err)
	}
}

func TestScanLines_StripsCarriageReturn(t *testing.T) {
	s := bufio.NewScanner(strings.NewReader("hello\r\nworld\r\n"))
	s.Split(ScanLinesAndTruncateWhenLongerThanBuffer)
	var got []string
	for s.Scan() {
		got = append(got, s.Text())
	}
	if len(got) != 2 || got[0] != "hello" || got[1] != "world" {
		t.Fatalf("got %v, want [hello world]", got)
	}
}
