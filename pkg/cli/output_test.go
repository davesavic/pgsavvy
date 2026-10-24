package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMachineCLI_OutputLimit(t *testing.T) {
	base := Envelope{OK: true, Command: "test", Data: ""}
	encoded, err := encode(base)
	if err != nil {
		t.Fatal(err)
	}
	base.Data = strings.Repeat("x", MaxOutputBytes-len(encoded))
	encoded, err = encode(base)
	if err != nil || len(encoded) != MaxOutputBytes {
		t.Fatalf("exact inclusive budget: %d %v", len(encoded), err)
	}
	base.Data = base.Data.(string) + "x"
	if _, err := encode(base); err == nil {
		t.Fatal("over budget accepted")
	}
	streams, out, _ := testStreams()
	_, exit := Dispatch(context.Background(), []string{"test"}, streams, map[string]Command{"test": func(context.Context, []string, IO) (Envelope, error) { return base, nil }})
	if exit != 7 || bytes.Count(out.Bytes(), []byte("\n")) != 1 || strings.Contains(out.String(), "xxxxx") {
		t.Fatalf("overflow published data: %d %s", exit, out.String())
	}
	for _, value := range []any{string([]byte{0xff}), []string{string([]byte{0xff})}, map[string]any{"nested": string([]byte{0xff})}} {
		if _, err := encode(Envelope{OK: true, Data: value}); err == nil {
			t.Fatal("invalid UTF-8 accepted")
		}
	}
	if _, err := encode(Envelope{OK: true, Warnings: []ErrorCode{"raw secret"}}); err == nil {
		t.Fatal("unknown warning accepted")
	}
	encoded, err = encode(Envelope{OK: true, Data: map[string]any{"rows": []string(nil), "null": (*string)(nil)}})
	if err != nil || !strings.Contains(string(encoded), `"rows":[]`) || !strings.Contains(string(encoded), `"null":null`) {
		t.Fatalf("nested empty arrays/null: %s %v", encoded, err)
	}
	encoded, err = encode(Envelope{OK: true, Data: (*string)(nil)})
	if err != nil || !strings.Contains(string(encoded), `"data":null`) {
		t.Fatalf("root null: %s %v", encoded, err)
	}
	encoded, err = encode(Envelope{Error: &PublicError{Code: InvalidInput}})
	if err != nil || strings.Contains(string(encoded), `"data"`) {
		t.Fatalf("error data must be absent: %s %v", encoded, err)
	}
	if _, err := encodeContext(&encodingCancelContext{Context: context.Background()}, Envelope{OK: true, Data: make([]int, 100)}); !errors.Is(err, context.Canceled) {
		t.Fatalf("encoding cancellation: %v", err)
	}
	if _, err := encode(Envelope{OK: true, Data: make([]bool, MaxOutputBytes)}); err == nil {
		t.Fatal("unbounded aggregate accepted")
	}
	streams, out, _ = testStreams()
	_, exit = Dispatch(context.Background(), []string{"test"}, streams, map[string]Command{"test": func(context.Context, []string, IO) (Envelope, error) {
		return Envelope{Command: string([]byte{0xff})}, nil
	}})
	if exit != 7 || !strings.Contains(out.String(), `"code":"RESOURCE_FAILURE"`) {
		t.Fatalf("invalid command fallback: %d %s", exit, out.String())
	}
}

func TestMachineCLI_EnvelopeLegacyBytes(t *testing.T) {
	for _, tc := range []struct {
		envelope Envelope
		want     string
	}{
		{Envelope{OK: true, Command: "query", Connection: "local", Data: map[string]any{"columns": []any{}, "rows": []any{}}, ResultComplete: true}, `{"schema_version":1,"ok":true,"command":"query","connection":"local","warnings":[],"result_complete":true,"truncated":false,"data":{"columns":[],"rows":[]}}` + "\n"},
		{Envelope{Command: "query", Error: &PublicError{Code: InvalidInput}}, `{"schema_version":1,"ok":false,"command":"query","error":{"code":"INVALID_INPUT","message":"Invalid command or input.","remediation":"Consult command help."},"warnings":[],"result_complete":false,"truncated":false}` + "\n"},
	} {
		got, err := encode(tc.envelope)
		if err != nil || string(got) != tc.want {
			t.Fatalf("legacy bytes changed: %s error=%v want=%s", got, err, tc.want)
		}
	}
}

type encodingCancelContext struct {
	context.Context
	checks int
}

func (ctx *encodingCancelContext) Err() error {
	ctx.checks++
	if ctx.checks >= 5 {
		return context.Canceled
	}
	return nil
}

type failingOutput struct {
	writes int
	short  bool
}

func (w *failingOutput) Write(data []byte) (int, error) {
	w.writes++
	if w.short {
		return len(data) - 1, nil
	}
	return 0, errors.New("SECRET backend SQL")
}
func (*failingOutput) Close() error { return nil }

func TestMachineCLI_BrokenWriter(t *testing.T) {
	for _, short := range []bool{false, true} {
		writer := &failingOutput{short: short}
		streams, _, diagnostic := testStreams()
		streams.Out = writer
		_, exit := Dispatch(context.Background(), []string{"test"}, streams, map[string]Command{"test": func(context.Context, []string, IO) (Envelope, error) { return Envelope{}, nil }})
		if exit != 7 || writer.writes != 1 || strings.Contains(diagnostic.String(), "SECRET") {
			t.Fatalf("writer failure: %d writes=%d", exit, writer.writes)
		}
		if !strings.Contains(diagnostic.String(), `"exit":7`) {
			t.Fatalf("audit recorded premature success: %s", diagnostic.String())
		}
	}
	streams, out, _ := testStreams()
	streams.Err = &failingOutput{}
	_, diagnosticExit := Dispatch(context.Background(), []string{"test"}, streams, map[string]Command{"test": func(context.Context, []string, IO) (Envelope, error) { return Envelope{}, nil }})
	if diagnosticExit != 0 || !strings.Contains(out.String(), `"ok":true`) {
		t.Fatalf("diagnostic failure suppressed stdout: %d %s", diagnosticExit, out.String())
	}
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_ = read.Close()
	if err := WriteJSON(context.Background(), write, Envelope{OK: true}); err == nil {
		t.Fatal("broken pipe succeeded")
	}
	_ = write.Close()
	read, write, err = machineOutputPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func(read, write *os.File) { _ = read.Close(); _ = write.Close() }(read, write)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = WriteJSON(ctx, write, Envelope{OK: true, Data: strings.Repeat("x", 512<<10)})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("open unread pipe did not cancel: %v elapsed=%s", err, time.Since(start))
	}
	read, write, err = machineOutputPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func(read, write *os.File) { _ = read.Close(); _ = write.Close() }(read, write)
	streams, _, _ = testStreams()
	streams.Out = write
	start = time.Now()
	_, exit := Dispatch(context.Background(), []string{"test", "--timeout=50ms"}, streams, map[string]Command{"test": func(context.Context, []string, IO) (Envelope, error) {
		return Envelope{Data: strings.Repeat("x", 512<<10)}, nil
	}})
	if exit != 7 || time.Since(start) > time.Second {
		t.Fatalf("dispatch unread pipe: exit %d elapsed %s", exit, time.Since(start))
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if err := WriteJSON(ctx, ioDiscardCloser{}, Envelope{OK: true}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled publication: %v", err)
	}
}

type ioDiscardCloser struct{}

func (ioDiscardCloser) Write(p []byte) (int, error) { return io.Discard.Write(p) }
func (ioDiscardCloser) Close() error                { return nil }
