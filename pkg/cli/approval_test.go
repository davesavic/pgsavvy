//go:build linux || darwin

package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type approvalTerminal struct {
	io.Reader
	bytes.Buffer
	closed bool
}

func (f *approvalTerminal) Read(p []byte) (int, error) { return f.Reader.Read(p) }
func (f *approvalTerminal) Close() error               { f.closed = true; return nil }

func approvalCode(err error) ErrorCode {
	if err == nil {
		return ""
	}
	var public *PublicError
	if errors.As(err, &public) {
		return public.Code
	}
	return "unexpected"
}

func TestMachineApprovalCeremony(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		want        ErrorCode
	}{
		{"prod", "prod\nyes\n", ""},
		{" 日本語 λ ", " 日本語 λ \r\nyes\r\n", ""},
		{"prod", "Prod\nyes\n", ApprovalMismatch},
		{"prod", "prod \nyes\n", ApprovalMismatch},
		{"prod", "prod\nYes\n", ApprovalCancelled},
		{"prod", "prod\nyes \n", ApprovalCancelled},
		{"prod", "prod\nno\n", ApprovalCancelled},
		{"prod", "", ApprovalCancelled},
		{"prod", "prod\nyes", ApprovalCancelled},
		{"", "\nyes\n", ApprovalUnavailable},
		{"prod\x1b[2J", "", ApprovalUnavailable},
		{"prod\u202e", "", ApprovalUnavailable},
	} {
		t.Run(tc.name+tc.input, func(t *testing.T) {
			terminal := &approvalTerminal{Reader: strings.NewReader(tc.input)}
			opens := 0
			provider := NewTerminalApprovalProvider(func(context.Context) (io.ReadWriteCloser, error) { opens++; return terminal, nil })
			err := provider(context.Background(), ApprovalRequest{Connection: tc.name, SQL: "DELETE FROM t", StatementCount: 1})
			if got := approvalCode(err); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
			if tc.want == ApprovalUnavailable {
				if opens != 0 {
					t.Fatal("unsafe name acquired terminal")
				}
				return
			}
			if opens != 1 || !terminal.closed {
				t.Fatal("terminal ownership not completed")
			}
		})
	}
}

func TestMachineApprovalPreview(t *testing.T) {
	request := ApprovalRequest{Connection: "prod", Schema: "x\x1b[2J\u202e", SQL: strings.Repeat("\x1b\x00\nλ", 3000), MaxRows: 17, ParameterCount: 3, StatementCount: 9, WritesApprovalRequired: true, DDLApprovalRequired: true}
	preview := approvalPreview(request)
	if len(preview) > 4096 {
		t.Fatalf("preview bytes=%d", len(preview))
	}
	for _, want := range []string{"omitted", "statements=9", "parameters=3", "max_rows=17", `\x1b`, `\u202e`, "writes=true", "ddl=true"} {
		if !strings.Contains(preview, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if strings.ContainsAny(preview, "\x1b\x00\r") || strings.Contains(preview, "\u202e") {
		t.Fatal("active terminal controls")
	}
}

func TestMachineApprovalIndependentReusable(t *testing.T) {
	for range 2 {
		calls := 0
		service := InvocationService{Approve: NewTerminalApprovalProvider(func(context.Context) (io.ReadWriteCloser, error) {
			calls++
			input := "prod\nyes\n"
			if calls == 2 {
				input = "prod\nno\n"
			}
			return &approvalTerminal{Reader: strings.NewReader(input)}, nil
		})}
		command := func() error { return service.Approve(context.Background(), ApprovalRequest{Connection: "prod"}) }
		if command() != nil || approvalCode(command()) != ApprovalCancelled || calls != 2 {
			t.Fatal("approval decision reused")
		}
	}
}

func TestMachineApprovalUnavailable(t *testing.T) {
	for _, open := range []func(context.Context) (io.ReadWriteCloser, error){nil, func(context.Context) (io.ReadWriteCloser, error) { return nil, errors.New("private native details") }} {
		if approvalCode(NewTerminalApprovalProvider(open)(context.Background(), ApprovalRequest{Connection: "prod"})) != ApprovalUnavailable {
			t.Fatal("terminal unavailable accepted")
		}
	}
}

type blockedApprovalTerminal struct {
	started, closed chan struct{}
	once            sync.Once
}

func (f *blockedApprovalTerminal) Read([]byte) (int, error) {
	f.once.Do(func() { close(f.started) })
	<-f.closed
	return 0, io.ErrClosedPipe
}
func (f *blockedApprovalTerminal) Write(p []byte) (int, error) { return len(p), nil }
func (f *blockedApprovalTerminal) Close() error                { close(f.closed); return nil }

func TestMachineApprovalCancellation(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		terminal := &blockedApprovalTerminal{started: make(chan struct{}), closed: make(chan struct{})}
		ctx, cancel := context.WithCancel(context.Background())
		if deadline {
			cancel()
			ctx, cancel = context.WithTimeout(context.Background(), 30*time.Millisecond)
		}
		provider := NewTerminalApprovalProvider(func(context.Context) (io.ReadWriteCloser, error) { return terminal, nil })
		done := make(chan error, 1)
		go func() { done <- provider(ctx, ApprovalRequest{Connection: "prod"}) }()
		select {
		case <-terminal.started:
		case <-time.After(time.Second):
			t.Fatal("read not started")
		}
		if !deadline {
			cancel()
		}
		select {
		case err := <-done:
			if approvalCode(err) != Timeout {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("approval failed to join")
		}
		cancel()
	}
}

type blockedApprovalWriter struct {
	started, released, joined chan struct{}
	release                   sync.Once
	closes, reads             atomic.Int32
}

func (f *blockedApprovalWriter) Read([]byte) (int, error) {
	f.reads.Add(1)
	return 0, io.ErrClosedPipe
}

func (f *blockedApprovalWriter) Write([]byte) (int, error) {
	close(f.started)
	<-f.released
	close(f.joined)
	return 0, io.ErrClosedPipe
}

func (f *blockedApprovalWriter) Close() error {
	f.closes.Add(1)
	f.release.Do(func() { close(f.released) })
	return nil
}

func TestMachineApprovalWriteLifecycle(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline", "closed"} {
		t.Run(mode, func(t *testing.T) {
			terminal := &blockedApprovalWriter{started: make(chan struct{}), released: make(chan struct{}), joined: make(chan struct{})}
			ctx, cancel := context.WithCancel(context.Background())
			if mode == "deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
			}
			if mode == "closed" {
				terminal.release.Do(func() { close(terminal.released) })
			}
			provider := NewTerminalApprovalProvider(func(context.Context) (io.ReadWriteCloser, error) { return terminal, nil })
			done := make(chan error, 1)
			returned := make(chan struct{})
			go func() {
				defer close(returned)
				done <- provider(ctx, ApprovalRequest{Connection: "prod"})
			}()
			t.Cleanup(func() {
				cancel()
				terminal.release.Do(func() { close(terminal.released) })
				select {
				case <-returned:
				case <-time.After(time.Second):
					t.Error("provider goroutine did not join during fixture cleanup")
				}
			})
			select {
			case <-terminal.started:
			case <-time.After(time.Second):
				t.Fatal("prompt write not started")
			}
			if mode == "cancel" {
				cancel()
			}
			want := Timeout
			if mode == "closed" {
				want = ApprovalUnavailable
			}
			select {
			case err := <-done:
				if got := approvalCode(err); got != want {
					t.Fatalf("got %s want %s", got, want)
				}
			case <-time.After(time.Second):
				t.Fatal("provider did not interrupt and join prompt write")
			}
			select {
			case <-terminal.joined:
			default:
				t.Fatal("provider returned before prompt write joined")
			}
			if terminal.closes.Load() != 1 || terminal.reads.Load() != 0 {
				t.Fatalf("close calls=%d read calls=%d", terminal.closes.Load(), terminal.reads.Load())
			}
		})
	}
}
