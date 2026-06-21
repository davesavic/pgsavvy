package pg

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/davesavic/pgsavvy/pkg/models"
)

// TestLiveTxStatusActiveTxTakesPrecedence verifies that an active driver-API
// transaction governs the reported status and surfaces its savepoint names,
// regardless of the cached raw-SQL status byte.
func TestLiveTxStatusActiveTxTakesPrecedence(t *testing.T) {
	s := newStubSession()
	s.cachedTxStatus.Store(uint32('I'))

	tx := newPgTransaction(&fakeTx{}, nil)
	require.NoError(t, tx.Savepoint(context.Background(), "sp1"))
	s.activeTx = tx

	status, sps := s.LiveTxStatus()
	require.Equal(t, models.TxActive, status)
	require.Equal(t, []string{"sp1"}, sps)
}

// TestLiveTxStatusActiveTxAborted verifies an aborted driver-API transaction
// reports TxAbortedInTx with its savepoints.
func TestLiveTxStatusActiveTxAborted(t *testing.T) {
	s := newStubSession()
	tx := newPgTransaction(&fakeTx{}, nil)
	require.NoError(t, tx.Savepoint(context.Background(), "sp1"))
	tx.status = models.TxAbortedInTx
	s.activeTx = tx

	status, sps := s.LiveTxStatus()
	require.Equal(t, models.TxAbortedInTx, status)
	require.Equal(t, []string{"sp1"}, sps)
}

// TestLiveTxStatusActiveTxTerminalFallsThrough verifies that a terminal
// driver-API transaction (committed / rolled back) does NOT report a status —
// there is no fallthrough to the cached byte from a non-cleared activeTx, and
// committed/rolled-back map to ("" , nil).
func TestLiveTxStatusActiveTxTerminalFallsThrough(t *testing.T) {
	for _, term := range []models.TxStatus{models.TxCommitted, models.TxRolledBack} {
		s := newStubSession()
		s.cachedTxStatus.Store(uint32('I'))
		tx := newPgTransaction(&fakeTx{}, nil)
		tx.status = term
		s.activeTx = tx

		status, sps := s.LiveTxStatus()
		require.Equal(t, models.TxStatus(""), status, "terminal status %s must report empty", term)
		require.Nil(t, sps)
	}
}

// TestLiveTxStatusRawSQLPath verifies that with no active driver-API tx, the
// cached pgconn status byte governs: 'T' → active, 'E' → aborted, 'I' → none.
// The raw-SQL path never carries savepoint names (D4).
func TestLiveTxStatusRawSQLPath(t *testing.T) {
	cases := []struct {
		b    byte
		want models.TxStatus
	}{
		{'T', models.TxActive},
		{'E', models.TxAbortedInTx},
		{'I', ""},
	}
	for _, c := range cases {
		s := newStubSession()
		s.cachedTxStatus.Store(uint32(c.b))
		status, sps := s.LiveTxStatus()
		require.Equal(t, c.want, status, "byte %q", c.b)
		require.Nil(t, sps)
	}
}

// TestSampleTxStatusNilPgConnDoesNotPanic verifies sampling on a stub session
// (nil pgConn) is a no-op and leaves the cached value untouched.
func TestSampleTxStatusNilPgConnDoesNotPanic(t *testing.T) {
	s := newStubSession()
	s.cachedTxStatus.Store(uint32('I'))
	require.NotPanics(t, s.sampleTxStatus)
	require.Equal(t, byte('I'), byte(s.cachedTxStatus.Load()))
}

// TestOnTerminateClearsActiveTx verifies the Begin-wired onTerminate callback
// nils activeTx so a subsequent LiveTxStatus reflects the live byte rather than
// a stale terminal transaction (Decision ①). The callback resamples; on a stub
// session the sample is a no-op, so we assert against the cached byte we set.
func TestOnTerminateClearsActiveTx(t *testing.T) {
	s := newStubSession()
	// Simulate the post-commit live byte (idle).
	s.cachedTxStatus.Store(uint32('I'))

	// Wire the same onTerminate Begin installs.
	onTerminate := func() {
		s.activeTx = nil
		s.sampleTxStatus()
	}
	tx := newPgTransaction(&fakeTx{}, onTerminate)
	s.activeTx = tx

	// While active: badge shows.
	status, _ := s.LiveTxStatus()
	require.Equal(t, models.TxActive, status)

	// Commit fires onTerminate → activeTx cleared → live byte ('I') governs.
	require.NoError(t, tx.Commit(context.Background()))
	require.Nil(t, s.activeTx)
	status, _ = s.LiveTxStatus()
	require.Equal(t, models.TxStatus(""), status)
}
