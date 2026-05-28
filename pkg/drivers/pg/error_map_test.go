package pg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"

	"github.com/davesavic/dbsavvy/pkg/drivers"
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

func TestIsConnectionDead(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"net.OpError", &net.OpError{Op: "read", Err: errors.New("reset")}, true},
		{"wrapped net.OpError", fmt.Errorf("pgx: %w", &net.OpError{Op: "write", Err: errors.New("broken")}), true},
		{"io.EOF", io.EOF, true},
		{"wrapped io.EOF", fmt.Errorf("pgx: %w", io.EOF), true},
		{"io.ErrUnexpectedEOF", io.ErrUnexpectedEOF, true},
		{"wrapped io.ErrUnexpectedEOF", fmt.Errorf("pgx: %w", io.ErrUnexpectedEOF), true},
		{"pgconn.PgError non-fatal", &pgconn.PgError{Code: "42P01", Severity: "ERROR"}, false},
		{"pgconn.PgError FATAL raw", &pgconn.PgError{Code: "57P01", Severity: "FATAL", Message: "admin shutdown"}, true},
		{"QueryError FATAL 57P01", wrapPgError(&pgconn.PgError{Code: "57P01", Severity: "FATAL"}), true},
		{"QueryError FATAL wrapped", fmt.Errorf("outer: %w", wrapPgError(&pgconn.PgError{Code: "57P01", Severity: "FATAL"})), true},
		{"QueryError non-FATAL", &drivers.QueryError{Raw: errors.New("x"), Severity: "ERROR"}, false},
		{"conn closed", errors.New("conn closed"), true},
		{"conn uninitialized", errors.New("conn uninitialized"), true},
		{"context.DeadlineExceeded", context.DeadlineExceeded, false},
		{"context.Canceled", context.Canceled, false},
		{"plain error", errors.New("something"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsConnectionDead(tc.err))
		})
	}
}
