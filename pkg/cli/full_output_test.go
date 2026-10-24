package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestMachineFullOutputApprovalCodes(t *testing.T) {
	for _, code := range []ErrorCode{"APPROVAL_REQUIRED", "APPROVAL_MISMATCH", "APPROVAL_UNAVAILABLE", "APPROVAL_CANCELLED", "INCOMPLETE_TRANSACTION"} {
		streams, out, diagnostic := testStreams()
		_, exit := Dispatch(context.Background(), []string{"query"}, streams, map[string]Command{"query": func(context.Context, []string, IO) (Envelope, error) {
			return Envelope{Data: "secret partial rows"}, &PublicError{Code: code, SQLState: "secret approval"}
		}})
		want := ExitPolicy
		if code == "INCOMPLETE_TRANSACTION" {
			want = ExitDatabase
		}
		var doc struct {
			Error struct{ Code ErrorCode }
		}
		if err := json.Unmarshal(out.Bytes(), &doc); err != nil || exit != want || doc.Error.Code != code {
			t.Fatalf("%s: exit=%d output=%s decode=%v", code, exit, out.String(), err)
		}
		if strings.Contains(out.String()+diagnostic.String(), "secret") || strings.Contains(out.String(), `"data"`) {
			t.Fatalf("private data published: %s %s", out.String(), diagnostic.String())
		}
	}
}

func fullOutputEnvelope(status string, effects bool) Envelope {
	return Envelope{Command: "query", Connection: "local", Execution: &executionMetadata{Status: status, EffectsMayHaveOccurred: effects, PreparedTransactionsMayRemain: effects}, ResultComplete: true}
}

func fullOutputDocument(t *testing.T, out *testOutput) map[string]any {
	t.Helper()
	if out.Len() > MaxOutputBytes || strings.Count(out.String(), "\n") != 1 {
		t.Fatalf("not one bounded document: bytes=%d", out.Len())
	}
	var doc map[string]any
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func assertFullExecution(t *testing.T, doc map[string]any, status string, effects bool) {
	t.Helper()
	metadata, ok := doc["execution"].(map[string]any)
	if !ok || metadata["status"] != status || metadata["effects_may_have_occurred"] != effects {
		t.Fatalf("execution metadata lost: %#v", doc)
	}
	if effects && metadata["prepared_transactions_may_remain"] != true {
		t.Fatalf("prepared transaction warning lost: %#v", doc)
	}
	if !effects {
		if _, exists := metadata["prepared_transactions_may_remain"]; exists {
			t.Fatalf("optional false field emitted: %#v", metadata)
		}
	}
}

func TestMachineFullOutputResults(t *testing.T) {
	command := map[string]any{"command_tag": "CREATE TABLE", "columns": []any{}, "rows": []any{}, "truncated": false}
	empty := map[string]any{"command_tag": "SELECT 0", "columns": []any{map[string]any{"name": "value", "type_oid": 25, "encoding": "postgres-text"}}, "rows": []any{}, "truncated": false}
	row := map[string]any{"command_tag": "SELECT 2", "columns": []any{map[string]any{"name": "value", "type_oid": 25, "encoding": "postgres-text"}}, "rows": []any{[]any{"kept", nil}}, "truncated": true, "truncation_reason": "row_limit"}
	for _, tc := range []struct {
		name    string
		results []any
		reason  string
	}{
		{"no_results", []any{}, ""},
		{"command_only", []any{command}, ""},
		{"empty_select", []any{empty}, ""},
		{"ordered_multiple", []any{command, empty}, ""},
		{"aggregate_rows", []any{command, row}, "row_limit"},
		{"result_limit", []any{command, empty}, "result_limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			streams, out, _ := testStreams()
			_, exit := Dispatch(context.Background(), []string{"query"}, streams, map[string]Command{"query": func(context.Context, []string, IO) (Envelope, error) {
				e := fullOutputEnvelope("completed", true)
				e.Data = map[string]any{"results": tc.results}
				e.Truncated, e.ResultComplete, e.TruncationReason = tc.reason != "", tc.reason == "", tc.reason
				return e, nil
			}})
			doc := fullOutputDocument(t, out)
			assertFullExecution(t, doc, "completed", true)
			want, _ := json.Marshal(map[string]any{"results": tc.results})
			got, _ := json.Marshal(doc["data"])
			if exit != ExitSuccess || string(got) != string(want) || doc["truncated"] != (tc.reason != "") || doc["result_complete"] != (tc.reason == "") || doc["schema_version"] != float64(1) {
				t.Fatalf("result changed: exit=%d %s want=%s", exit, out.String(), want)
			}
			if tc.reason != "" && doc["truncation_reason"] != tc.reason {
				t.Fatalf("missing truncation reason: %s", out.String())
			}
		})
	}
	if _, err := encode(Envelope{OK: true, Truncated: true, TruncationReason: "result_limit"}); err == nil {
		t.Fatal("old mode gained full-only reason")
	}
	if _, err := encode(Envelope{OK: true, Execution: &executionMetadata{Status: "completed"}, TruncationReason: "max_results"}); err == nil {
		t.Fatal("driver-internal reason escaped")
	}
}

func TestMachineFullOutputErrorRetention(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		effects      bool
		err          error
		code         ErrorCode
		exit         int
	}{
		{"cleanup", "completed", true, &PublicError{Code: ResourceFailure}, ResourceFailure, 7},
		{"deferred_sql", "failed", true, &PublicError{Code: DatabaseFailure, SQLState: "23505"}, DatabaseFailure, 5},
		{"incomplete_transaction", "failed", true, &PublicError{Code: IncompleteTransaction}, IncompleteTransaction, 5},
		{"transport_uncertainty", "unknown", true, errors.New("SECRET backend SQL parameter password"), ResourceFailure, 7},
		{"preflight_no_dispatch", "not_started", false, &PublicError{Code: UnsupportedSQL}, UnsupportedSQL, 2},
		{"approval_no_dispatch", "not_started", false, &PublicError{Code: ApprovalRequired}, ApprovalRequired, 4},
		{"cancellation", "unknown", true, context.Canceled, Timeout, 6},
		{"overflow", "completed", true, nil, ResourceFailure, 7},
		{"invalid_command", "completed", true, nil, ResourceFailure, 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			streams, out, diagnostic := testStreams()
			_, exit := Dispatch(context.Background(), []string{"query"}, streams, map[string]Command{"query": func(context.Context, []string, IO) (Envelope, error) {
				e := fullOutputEnvelope(tc.status, tc.effects)
				e.Data = map[string]any{"results": []any{map[string]any{"rows": []string{"SECRET partial rows"}}}}
				e.Truncated, e.TruncationReason = true, "result_limit"
				if tc.name == "overflow" {
					e.Data = strings.Repeat("x", MaxOutputBytes)
				}
				if tc.name == "invalid_command" {
					e.Command = string([]byte{0xff})
				}
				if tc.name == "deferred_sql" {
					e.Error = tc.err.(*PublicError)
					return e, nil
				}
				return e, tc.err
			}})
			doc := fullOutputDocument(t, out)
			assertFullExecution(t, doc, tc.status, tc.effects)
			if exit != tc.exit || doc["error"].(map[string]any)["code"] != string(tc.code) || doc["result_complete"] != false || doc["truncated"] != false {
				t.Fatalf("error output: exit=%d %s", exit, out.String())
			}
			if doc["data"] != nil || doc["truncation_reason"] != nil || strings.Contains(out.String()+diagnostic.String(), "SECRET") {
				t.Fatalf("partial/private data leaked: %s %s", out.String(), diagnostic.String())
			}
		})
	}
}

func TestMachineFullOutputStatusAllowlist(t *testing.T) {
	for _, status := range []string{"not_started", "completed", "failed", "unknown", "", "SECRET SQL\n\xff"} {
		e := fullOutputEnvelope(status, true)
		e.OK = true
		data, err := encode(e)
		if err != nil {
			t.Fatal(err)
		}
		want := status
		if status == "" || strings.HasPrefix(status, "SECRET") {
			want = "unknown"
		}
		out := &testOutput{}
		_, _ = out.Write(data)
		assertFullExecution(t, fullOutputDocument(t, out), want, true)
		if strings.Contains(string(data), "SECRET") || e.Execution.Status != status {
			t.Fatal("raw status leaked or caller metadata mutated")
		}
	}
}

type fullOutputDeadlineContext struct {
	context.Context
	checks int
}

func (ctx *fullOutputDeadlineContext) Err() error {
	ctx.checks++
	if ctx.checks >= 2 {
		return context.DeadlineExceeded
	}
	return nil
}

func TestMachineFullOutputEncodingBoundary(t *testing.T) {
	e := fullOutputEnvelope("completed", true)
	e.OK, e.Data = true, ""
	data, err := encode(e)
	if err != nil {
		t.Fatal(err)
	}
	e.Data = strings.Repeat("x", MaxOutputBytes-len(data))
	data, err = encode(e)
	if err != nil || len(data) != MaxOutputBytes {
		t.Fatalf("inclusive full envelope limit: bytes=%d err=%v", len(data), err)
	}
	e.Data = e.Data.(string) + "x"
	if _, err := encode(e); err == nil {
		t.Fatal("full envelope exceeded inclusive limit")
	}
	// The outer budget can expire after normalization, at the buffered write.
	e.Data = ""
	data, err = encodeContext(&fullOutputDeadlineContext{Context: context.Background()}, e)
	if !errors.Is(err, context.DeadlineExceeded) || len(data) != 0 {
		t.Fatalf("encoding deadline misclassified or published: bytes=%d err=%v", len(data), err)
	}
}

func TestMachineFullOutputCancellationAndDeadlines(t *testing.T) {
	t.Run("signal_before_publication", func(t *testing.T) {
		streams, out, _ := testStreams()
		signal := make(chan struct{})
		streams.Cancel = signal
		_, exit := Dispatch(context.Background(), []string{"query"}, streams, map[string]Command{"query": func(ctx context.Context, _ []string, _ IO) (Envelope, error) {
			ctx.Value(commandFinalizationKey{}).(*commandFinalization).run = func(time.Time) []ErrorCode { close(signal); return nil }
			return fullOutputEnvelope("completed", true), nil
		}})
		doc := fullOutputDocument(t, out)
		assertFullExecution(t, doc, "completed", true)
		if exit != ExitTimeout || doc["error"].(map[string]any)["code"] != "TIMEOUT" || doc["data"] != nil {
			t.Fatalf("signal output: exit=%d %s", exit, out.String())
		}
	})
	t.Run("work_deadline_retains_reserve", func(t *testing.T) {
		streams, out, _ := testStreams()
		_, exit := Dispatch(context.Background(), []string{"query", "--timeout=100ms"}, streams, map[string]Command{"query": func(ctx context.Context, _ []string, _ IO) (Envelope, error) {
			<-ctx.Done()
			return fullOutputEnvelope("unknown", true), nil
		}})
		assertFullExecution(t, fullOutputDocument(t, out), "unknown", true)
		if exit != ExitTimeout {
			t.Fatalf("work deadline exit=%d", exit)
		}
	})
	t.Run("outer_deadline_during_finalization", func(t *testing.T) {
		parent, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		streams, out, _ := testStreams()
		_, exit := Dispatch(parent, []string{"query"}, streams, map[string]Command{"query": func(ctx context.Context, _ []string, _ IO) (Envelope, error) {
			ctx.Value(commandFinalizationKey{}).(*commandFinalization).run = func(time.Time) []ErrorCode { <-parent.Done(); return nil }
			return fullOutputEnvelope("completed", true), nil
		}})
		if exit != ExitTimeout || out.Len() != 0 {
			t.Fatalf("exhausted outer context published: exit=%d %s", exit, out.String())
		}
	})
	t.Run("deadline_during_encoding", func(t *testing.T) {
		e := fullOutputEnvelope("completed", true)
		e.OK, e.Data = true, make([]int, 100)
		data, err := encodeContext(&encodingCancelContext{Context: context.Background()}, e)
		if !errors.Is(err, context.Canceled) || len(data) != 0 || e.Execution.Status != "completed" {
			t.Fatalf("cancelled encoding published or changed execution: bytes=%d err=%v", len(data), err)
		}
	})
}

type fullOutputSignalWriter struct {
	io.Writer
	signal  chan struct{}
	writes  int
	partial bool
}

func (w *fullOutputSignalWriter) Write(data []byte) (int, error) {
	w.writes++
	close(w.signal)
	if w.partial {
		return w.Writer.Write(data[:len(data)/2])
	}
	return w.Writer.Write(data)
}
func (*fullOutputSignalWriter) Close() error { return nil }

func TestMachineFullOutputBrokenWriterAndSignalAfterPublication(t *testing.T) {
	for _, partial := range []bool{false, true} {
		streams, out, _ := testStreams()
		writer := &fullOutputSignalWriter{Writer: out, signal: make(chan struct{}), partial: partial}
		streams.Out, streams.Cancel = writer, writer.signal
		_, exit := Dispatch(context.Background(), []string{"query"}, streams, map[string]Command{"query": func(context.Context, []string, IO) (Envelope, error) {
			return fullOutputEnvelope("completed", true), nil
		}})
		if writer.writes != 1 {
			t.Fatalf("replacement write attempted: %d", writer.writes)
		}
		if partial {
			if exit != ExitTimeout || json.Valid(out.Bytes()) {
				t.Fatalf("partial signal output: exit=%d %s", exit, out.String())
			}
		} else {
			doc := fullOutputDocument(t, out)
			assertFullExecution(t, doc, "completed", true)
			if exit != ExitSuccess || doc["ok"] != true {
				t.Fatalf("post-commit signal replaced success: exit=%d %s", exit, out.String())
			}
		}
	}
	for _, short := range []bool{false, true} {
		streams, _, diagnostic := testStreams()
		writer := &failingOutput{short: short}
		streams.Out = writer
		_, exit := Dispatch(context.Background(), []string{"query"}, streams, map[string]Command{"query": func(context.Context, []string, IO) (Envelope, error) {
			return fullOutputEnvelope("completed", true), nil
		}})
		if exit != ExitResource || writer.writes != 1 || strings.Contains(diagnostic.String(), "SECRET") {
			t.Fatalf("broken writer: exit=%d writes=%d diagnostics=%s", exit, writer.writes, diagnostic.String())
		}
	}
}
