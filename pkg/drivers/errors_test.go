package drivers

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

func TestErrNotImplemented_SelfMatches(t *testing.T) {
	if !errors.Is(ErrNotImplemented, ErrNotImplemented) {
		t.Fatal("errors.Is(ErrNotImplemented, ErrNotImplemented) must be true")
	}
}

func TestErrNotImplemented_NotErrUnsupported(t *testing.T) {
	if errors.Is(ErrNotImplemented, errors.ErrUnsupported) {
		t.Fatal("ErrNotImplemented must not alias errors.ErrUnsupported (epic dbsavvy-921 D4)")
	}
}

func TestErrNotImplemented_DistinctFromCommonSentinels(t *testing.T) {
	for _, other := range []error{io.EOF, sql.ErrNoRows, os.ErrNotExist} {
		if errors.Is(ErrNotImplemented, other) {
			t.Fatalf("ErrNotImplemented must not match unrelated sentinel %v", other)
		}
	}
}

func TestErrNotImplemented_PropagatesThroughWrap(t *testing.T) {
	wrapped := fmt.Errorf("session: execute: %w", ErrNotImplemented)
	if !errors.Is(wrapped, ErrNotImplemented) {
		t.Fatal("a wrapped ErrNotImplemented must satisfy errors.Is")
	}
}

func TestQueryError_NilRawDoesNotPanic(t *testing.T) {
	qe := &QueryError{Code: "42P01", Severity: "ERROR", Hint: "check the schema"}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("QueryError.Error() panicked with Raw=nil: %v", r)
		}
	}()
	msg := qe.Error()
	if !strings.Contains(msg, "42P01") {
		t.Fatalf("expected message to include Code, got %q", msg)
	}
}

func TestQueryError_EmptyDoesNotPanic(t *testing.T) {
	qe := &QueryError{}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("QueryError.Error() panicked on empty struct: %v", r)
		}
	}()
	_ = qe.Error()
}

func TestQueryError_NilReceiverError(t *testing.T) {
	var qe *QueryError
	if got := qe.Error(); got != "<nil>" {
		t.Fatalf("nil receiver Error() = %q, want <nil>", got)
	}
}

func TestQueryError_UnwrapReturnsRaw(t *testing.T) {
	raw := errors.New("boom")
	qe := &QueryError{Raw: raw}
	if got := qe.Unwrap(); got != raw {
		t.Fatalf("Unwrap got %v want %v", got, raw)
	}
	if !errors.Is(qe, raw) {
		t.Fatal("errors.Is must traverse QueryError.Unwrap to Raw")
	}
}

func TestQueryError_NilReceiverUnwrap(t *testing.T) {
	var qe *QueryError
	if got := qe.Unwrap(); got != nil {
		t.Fatalf("nil receiver Unwrap = %v, want nil", got)
	}
}

func TestQueryError_RawSurfacedInError(t *testing.T) {
	qe := &QueryError{Raw: errors.New("relation does not exist"), Hint: "did you forget a schema qualifier?"}
	msg := qe.Error()
	if !strings.Contains(msg, "relation does not exist") {
		t.Fatalf("expected Raw to be surfaced, got %q", msg)
	}
	if !strings.Contains(msg, "hint:") {
		t.Fatalf("expected hint to be appended, got %q", msg)
	}
}
