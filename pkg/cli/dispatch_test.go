package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type testOutput struct{ bytes.Buffer }

func (*testOutput) Close() error { return nil }

func testStreams() (IO, *testOutput, *testOutput) {
	out, diagnostic := &testOutput{}, &testOutput{}
	return IO{In: io.NopCloser(strings.NewReader("")), Out: out, Err: diagnostic}, out, diagnostic
}

func TestMachineCLI_Dispatch(t *testing.T) {
	for _, args := range [][]string{nil, {"--version"}, {"-version"}, {"--log-dir", "private"}, {"--log-dir=private"}, {"update"}} {
		streams, out, diag := testStreams()
		handled, exit := Dispatch(context.Background(), args, streams, nil)
		if handled || exit != 0 || out.Len() != 0 || diag.Len() != 0 {
			t.Fatalf("legacy %v was handled", args)
		}
	}
	for _, args := range [][]string{{"unknown-secret"}, {"--bad"}, {"test", "--format", "yaml"}, {"test", "--timeout", "0s"}, {"test", "--timeout", "2147483648ms"}, {"test", "--timeout"}, {"test", "--x=1", "--x", "2"}, {"test", "--format=json", "--format=json"}} {
		called := false
		streams, out, diag := testStreams()
		handled, exit := Dispatch(context.Background(), args, streams, map[string]Command{"test": func(context.Context, []string, IO) (Envelope, error) { called = true; return Envelope{}, nil }})
		if !handled || exit != ExitInput || called {
			t.Fatalf("invalid %v: handled=%v exit=%d called=%v", args, handled, exit, called)
		}
		if strings.Contains(out.String()+diag.String(), "unknown-secret") {
			t.Fatal("echoed unknown input")
		}
	}
	streams, out, _ := testStreams()
	handled, exit := Dispatch(context.Background(), []string{"test", "--format=json", "--timeout=1s", "literal.name"}, streams, map[string]Command{"test": func(ctx context.Context, args []string, _ IO) (Envelope, error) {
		if len(args) != 1 || args[0] != "literal.name" || CleanupBudget(ctx) > 200*time.Millisecond {
			t.Errorf("incorrect invocation context/args: %v", args)
		}
		return Envelope{Data: []string{}, ResultComplete: true}, nil
	}})
	if !handled || exit != 0 || !strings.Contains(out.String(), `"data":[]`) {
		t.Fatalf("dispatch success: %d %s", exit, out.String())
	}
	streams, out, _ = testStreams()
	_, exit = Dispatch(context.Background(), []string{"--help"}, streams, map[string]Command{"test": nil})
	if exit != 0 || !strings.Contains(out.String(), `"commands":["test"]`) {
		t.Fatalf("help: %d %s", exit, out.String())
	}
	for _, args := range [][]string{{"--bad"}, {"--schema"}, {"--schema=a", "--schema=b"}, {"--include-system=false"}, {"--schema", "--bad"}} {
		if _, _, err := ParseFlags(args, []string{"schema"}, []string{"include-system"}); err == nil {
			t.Errorf("invalid flags accepted: %v", args)
		}
	}
	flags, positional, err := ParseFlags([]string{"NAME.with.dots", "--schema=Exact.Name", "--include-system"}, []string{"schema"}, []string{"include-system"})
	if err != nil || len(positional) != 1 || positional[0] != "NAME.with.dots" || flags["schema"] != "Exact.Name" || flags["include-system"] != "true" {
		t.Fatalf("flags: %v %v %v", flags, positional, err)
	}
}

func TestMachineCLI_ExitCodes(t *testing.T) {
	for _, tc := range []struct {
		err  error
		exit int
	}{
		{nil, 0},
		{&PublicError{Code: InvalidInput}, 2},
		{&PublicError{Code: UnsupportedSQL}, 2},
		{&PublicError{Code: ConnectionUnavailable}, 3},
		{&PublicError{Code: CredentialsUnavailable}, 3},
		{&PublicError{Code: TrustRequired}, 3},
		{&PublicError{Code: UnsupportedPgpassFailover}, 3},
		{&PublicError{Code: PolicyDenied}, 4},
		{&PublicError{Code: "APPROVAL_REQUIRED"}, 4},
		{&PublicError{Code: "APPROVAL_MISMATCH"}, 4},
		{&PublicError{Code: "APPROVAL_UNAVAILABLE"}, 4},
		{&PublicError{Code: "APPROVAL_CANCELLED"}, 4},
		{&PublicError{Code: "INCOMPLETE_TRANSACTION"}, 5},
		{&PublicError{Code: DatabaseFailure}, 5},
		{&PublicError{Code: ObjectNotFound}, 5},
		{context.Canceled, 6},
		{context.DeadlineExceeded, 6},
		{errors.New("raw SQL DSN helper-secret"), 7},
		{&PublicError{Code: "helper-secret"}, 7},
	} {
		streams, out, diagnostic := testStreams()
		_, exit := Dispatch(context.Background(), []string{"test"}, streams, map[string]Command{"test": func(context.Context, []string, IO) (Envelope, error) { return Envelope{}, tc.err }})
		if exit != tc.exit {
			t.Fatalf("%T: exit %d want %d: %s", tc.err, exit, tc.exit, out.String())
		}
		text := out.String() + diagnostic.String()
		for _, secret := range []string{"raw SQL", "DSN", "helper-secret"} {
			if strings.Contains(text, secret) {
				t.Fatalf("leaked %s", secret)
			}
		}
		var doc map[string]any
		if err := json.Unmarshal(out.Bytes(), &doc); err != nil || doc["schema_version"] != float64(1) || doc["ok"] != (tc.exit == 0) {
			t.Fatalf("bad result: %s, %v", out.String(), err)
		}
	}
}

func TestMachineCLI_Envelope(t *testing.T) {
	streams, out, _ := testStreams()
	cleaned := false
	_, exit := Dispatch(context.Background(), []string{"test"}, streams, map[string]Command{"test": func(context.Context, []string, IO) (Envelope, error) {
		defer func() { cleaned = true }()
		if out.Len() != 0 {
			t.Error("serialized before cleanup")
		}
		return Envelope{Data: []string{}, ResultComplete: true}, nil
	}})
	if !cleaned || exit != 0 || !strings.HasSuffix(out.String(), "\n") || strings.Count(out.String(), "\n") != 1 || !strings.Contains(out.String(), `"warnings":[]`) {
		t.Fatalf("envelope: %s", out.String())
	}
	streams, out, _ = testStreams()
	_, exit = Dispatch(context.Background(), []string{"test", "--timeout=50ms"}, streams, map[string]Command{"test": func(ctx context.Context, _ []string, _ IO) (Envelope, error) {
		<-ctx.Done()
		return Envelope{}, ctx.Err()
	}})
	if exit != ExitTimeout || !strings.Contains(out.String(), `"code":"TIMEOUT"`) {
		t.Fatalf("timeout publication: %d %s", exit, out.String())
	}
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			streams, out, _ := testStreams()
			_, exit := Dispatch(context.Background(), []string{"test"}, streams, map[string]Command{"test": func(context.Context, []string, IO) (Envelope, error) { return Envelope{Data: []string{}}, nil }})
			if exit != 0 || !strings.Contains(out.String(), `"data":[]`) {
				t.Error("concurrent invocation failed")
			}
		})
	}
	wg.Wait()
}

func TestMachineCLI_Envelope_ParentDeadlineAndInput(t *testing.T) {
	read, write, err := machineInputPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = read.Close(); _ = write.Close() }()
	streams, out, _ := testStreams()
	streams.In = read
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	cleaned := false
	_, exit := Dispatch(ctx, []string{"test", "--timeout=2m"}, streams, map[string]Command{"test": func(ctx context.Context, _ []string, streams IO) (Envelope, error) {
		if CleanupBudget(ctx) > 20*time.Millisecond {
			t.Errorf("parent deadline reserve exceeds effective budget: %s", CleanupBudget(ctx))
		}
		_, err := streams.In.Read(make([]byte, 1))
		cleaned = true
		return Envelope{}, err
	}})
	if exit != 6 {
		t.Fatal("input lost reserve: exit status")
	}
	if !cleaned {
		t.Fatal("input lost reserve: cleanup incomplete")
	}
	if time.Since(start) >= 100*time.Millisecond {
		t.Fatal("input lost reserve: parent deadline reached")
	}
	if !strings.Contains(out.String(), `"code":"TIMEOUT"`) {
		t.Fatal("input lost reserve: timeout publication missing")
	}
}

func TestMachineCLI_TimeoutBounds(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{
		{"1ns", time.Nanosecond},
		{"500us", 500 * time.Microsecond},
		{"31s", 31 * time.Second},
		{"2m", 2 * time.Minute},
		{"2147483647ms", MaxInvocationTimeout},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			args := []string{"test", "--timeout=" + tc.raw}
			_, options, err := invocationFlags(args[1:])
			if err != nil || options.timeout != tc.want || options.statementTimeout != tc.want || InvocationBudget(args) != tc.want {
				t.Fatalf("timeout %q: options=%+v, err=%v", tc.raw, options, err)
			}
		})
	}
	for _, raw := range []string{"0", "-1s", "bad", "2147483647.000001ms", "2147483648ms", "999999999999999999999h"} {
		args := []string{"test", "--timeout=" + raw}
		if _, _, err := invocationFlags(args[1:]); err == nil || InvocationBudget(args) != InvocationTimeout {
			t.Fatalf("invalid timeout %q accepted", raw)
		}
	}
	if _, options, err := invocationFlags(nil); err != nil || options.timeout != InvocationTimeout || options.statementTimeout != StatementTimeout {
		t.Fatalf("defaults changed: %+v, %v", options, err)
	}
	if InvocationBudget(nil) != InvocationTimeout || InvocationBudget([]string{"test", "--timeout=2m", "--timeout=1m"}) != InvocationTimeout {
		t.Fatal("invalid invocation lost default error-publication budget")
	}
}
