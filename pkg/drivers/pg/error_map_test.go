package pg

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// TestIsStatementTimeout_DeadlineExceeded confirms a context.DeadlineExceeded
// (the error a context.WithTimeout ceiling surfaces) classifies as a
// statement timeout, distinct from a user cancel.
func TestIsStatementTimeout_DeadlineExceeded(t *testing.T) {
	if !IsStatementTimeout(context.DeadlineExceeded) {
		t.Fatal("IsStatementTimeout(context.DeadlineExceeded) = false, want true")
	}
}

// TestIsStatementTimeout_WrappedDeadlineExceeded confirms the classifier
// traverses wrapping (pgx wraps the ctx error in its own error type), so a
// wrapped DeadlineExceeded still classifies as a timeout.
func TestIsStatementTimeout_WrappedDeadlineExceeded(t *testing.T) {
	wrapped := fmt.Errorf("pgx: read tcp: %w", context.DeadlineExceeded)
	if !IsStatementTimeout(wrapped) {
		t.Fatal("IsStatementTimeout(wrapped DeadlineExceeded) = false, want true")
	}
}

// TestIsStatementTimeout_CanceledIsNotTimeout is the distinguishing case: a
// user <leader>x / preemption surfaces context.Canceled, which must NOT be
// classified as a statement timeout.
func TestIsStatementTimeout_CanceledIsNotTimeout(t *testing.T) {
	if IsStatementTimeout(context.Canceled) {
		t.Fatal("IsStatementTimeout(context.Canceled) = true, want false (user cancel, not timeout)")
	}
	wrapped := fmt.Errorf("pgx: %w", context.Canceled)
	if IsStatementTimeout(wrapped) {
		t.Fatal("IsStatementTimeout(wrapped Canceled) = true, want false")
	}
}

// TestIsStatementTimeout_NilAndOther confirms nil and unrelated errors are
// not timeouts.
func TestIsStatementTimeout_NilAndOther(t *testing.T) {
	if IsStatementTimeout(nil) {
		t.Fatal("IsStatementTimeout(nil) = true, want false")
	}
	if IsStatementTimeout(errors.New("boom")) {
		t.Fatal("IsStatementTimeout(other) = true, want false")
	}
}

// TestStatementTimeoutMessage pins the verbatim cancel-reason string the tab
// surfaces for a timed-out stream (dbsavvy-fow.7 U15 AC: the tab shows
// "cancelled: statement timeout").
func TestStatementTimeoutMessage(t *testing.T) {
	if StatementTimeoutMessage != "cancelled: statement timeout" {
		t.Fatalf("StatementTimeoutMessage = %q, want %q", StatementTimeoutMessage, "cancelled: statement timeout")
	}
}
