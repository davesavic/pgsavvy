package pg

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/pgsavvy/pkg/models"
)

// fakeTx is a minimal pgx.Tx fake for unit-testing pgTransaction methods
// without a live database. Only Commit, Rollback, and Exec are exercised.
type fakeTx struct {
	pgx.Tx
	commitErr   error
	rollbackErr error
	execErr     error
	execCalls   []string
}

func (f *fakeTx) Commit(_ context.Context) error   { return f.commitErr }
func (f *fakeTx) Rollback(_ context.Context) error { return f.rollbackErr }
func (f *fakeTx) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	f.execCalls = append(f.execCalls, sql)
	return pgconn.NewCommandTag(""), f.execErr
}

func TestTransactionCommit(t *testing.T) {
	ft := &fakeTx{}
	tx := newPgTransaction(ft, nil)
	require.Equal(t, models.TxActive, tx.Status())

	err := tx.Commit(context.Background())
	require.NoError(t, err)
	require.Equal(t, models.TxCommitted, tx.Status())
}

func TestTransactionMetadataConcurrentStatusPolling(t *testing.T) {
	tx := newPgTransaction(&fakeTx{}, nil)
	sess := &Session{activeTx: tx}
	started, stop := make(chan struct{}), make(chan struct{})
	var readers sync.WaitGroup
	readers.Add(1)
	go func() {
		defer readers.Done()
		close(started)
		for {
			select {
			case <-stop:
				return
			default:
				sess.LiveTxStatus()
				sess.CurrentTransaction()
				tx.Status()
				tx.Savepoints()
			}
		}
	}()
	<-started
	for i := range 1000 {
		name := fmt.Sprint(i)
		require.NoError(t, tx.Savepoint(context.Background(), name))
		tx.ObserveError(&pgconn.PgError{Code: "25P02"})
		require.NoError(t, tx.Release(context.Background(), name))
	}
	require.NoError(t, tx.Rollback(context.Background()))
	close(stop)
	readers.Wait()
	require.Equal(t, models.TxRolledBack, tx.Status())
}

type statusBarrierTx struct {
	*fakeTx
	entered, release chan struct{}
}

func (tx *statusBarrierTx) Rollback(context.Context) error {
	close(tx.entered)
	<-tx.release
	return nil
}

func TestTransactionStatusPollingDoesNotWaitForWire(t *testing.T) {
	barrier := &statusBarrierTx{fakeTx: &fakeTx{}, entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-barrier.release:
		default:
			close(barrier.release)
		}
	})
	tx := newPgTransaction(barrier, nil)
	sess := &Session{activeTx: tx}
	done := make(chan error, 1)
	go func() { done <- tx.Rollback(context.Background()) }()
	<-barrier.entered
	polled := make(chan models.TxStatus, 1)
	go func() { state, _ := sess.LiveTxStatus(); polled <- state }()
	select {
	case state := <-polled:
		require.Equal(t, models.TxActive, state)
	case <-time.After(time.Second):
		t.Fatal("status polling blocked on rollback wire wait")
	}
	close(barrier.release)
	require.NoError(t, <-done)
}

func TestTransactionCommitRejectsNonActive(t *testing.T) {
	ft := &fakeTx{}
	tx := newPgTransaction(ft, nil)
	tx.status = models.TxCommitted
	err := tx.Commit(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot commit")
}

func TestTransactionRollback(t *testing.T) {
	ft := &fakeTx{}
	tx := newPgTransaction(ft, nil)

	err := tx.Rollback(context.Background())
	require.NoError(t, err)
	require.Equal(t, models.TxRolledBack, tx.Status())
}

func TestTransactionRollbackFromAbortedState(t *testing.T) {
	ft := &fakeTx{}
	tx := newPgTransaction(ft, nil)
	tx.status = models.TxAbortedInTx

	err := tx.Rollback(context.Background())
	require.NoError(t, err)
	require.Equal(t, models.TxRolledBack, tx.Status())
}

func TestTransactionRollbackRejectsCommitted(t *testing.T) {
	ft := &fakeTx{}
	tx := newPgTransaction(ft, nil)
	tx.status = models.TxCommitted

	err := tx.Rollback(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot rollback")
}

func TestTransactionSavepoint(t *testing.T) {
	ft := &fakeTx{}
	tx := newPgTransaction(ft, nil)

	err := tx.Savepoint(context.Background(), "sp1")
	require.NoError(t, err)
	require.Equal(t, []string{"sp1"}, tx.Savepoints())
	require.Len(t, ft.execCalls, 1)
	require.Contains(t, ft.execCalls[0], "SAVEPOINT")
	require.Equal(t, 1, tx.StatementCount())
}

func TestTransactionSavepointEmptyNameReturnsError(t *testing.T) {
	ft := &fakeTx{}
	tx := newPgTransaction(ft, nil)

	err := tx.Savepoint(context.Background(), "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "must not be empty")
}

func TestTransactionSavepointInjectionPayloadSafelyQuoted(t *testing.T) {
	ft := &fakeTx{}
	tx := newPgTransaction(ft, nil)

	err := tx.Savepoint(context.Background(), "sp1; DROP TABLE users; --")
	require.NoError(t, err)
	require.Len(t, ft.execCalls, 1)
	// pgx.Identifier.Sanitize() double-quotes the name, making the injection
	// payload a harmless identifier rather than executable SQL.
	assert.Equal(t, `SAVEPOINT "sp1; DROP TABLE users; --"`, ft.execCalls[0])
}

func TestTransactionRelease(t *testing.T) {
	ft := &fakeTx{}
	tx := newPgTransaction(ft, nil)

	require.NoError(t, tx.Savepoint(context.Background(), "sp1"))
	require.NoError(t, tx.Savepoint(context.Background(), "sp2"))
	require.Equal(t, []string{"sp1", "sp2"}, tx.Savepoints())

	err := tx.Release(context.Background(), "sp1")
	require.NoError(t, err)
	require.Equal(t, []string{"sp2"}, tx.Savepoints())
	require.Contains(t, ft.execCalls[2], "RELEASE SAVEPOINT")
}

func TestTransactionReleaseEmptyNameReturnsError(t *testing.T) {
	ft := &fakeTx{}
	tx := newPgTransaction(ft, nil)
	err := tx.Release(context.Background(), "")
	require.Error(t, err)
}

func TestTransactionRollbackTo(t *testing.T) {
	ft := &fakeTx{}
	tx := newPgTransaction(ft, nil)

	require.NoError(t, tx.Savepoint(context.Background(), "sp1"))

	err := tx.RollbackTo(context.Background(), "sp1")
	require.NoError(t, err)
	require.Contains(t, ft.execCalls[1], "ROLLBACK TO SAVEPOINT")
}

func TestTransactionRollbackToEmptyNameReturnsError(t *testing.T) {
	ft := &fakeTx{}
	tx := newPgTransaction(ft, nil)
	err := tx.RollbackTo(context.Background(), "")
	require.Error(t, err)
}

func TestTransactionObserveError25P02(t *testing.T) {
	ft := &fakeTx{}
	tx := newPgTransaction(ft, nil)
	require.Equal(t, models.TxActive, tx.Status())

	tx.ObserveError(&pgconn.PgError{Code: "25P02"})
	require.Equal(t, models.TxAbortedInTx, tx.Status())
}

func TestTransactionObserveErrorNon25P02IsNoop(t *testing.T) {
	ft := &fakeTx{}
	tx := newPgTransaction(ft, nil)

	tx.ObserveError(&pgconn.PgError{Code: "42P01"})
	require.Equal(t, models.TxActive, tx.Status())
}

func TestTransactionObserveErrorNilIsNoop(t *testing.T) {
	ft := &fakeTx{}
	tx := newPgTransaction(ft, nil)
	tx.ObserveError(nil)
	require.Equal(t, models.TxActive, tx.Status())
}

func TestTransactionObserveErrorPlainErrorIsNoop(t *testing.T) {
	ft := &fakeTx{}
	tx := newPgTransaction(ft, nil)
	tx.ObserveError(errors.New("something"))
	require.Equal(t, models.TxActive, tx.Status())
}

func TestTransactionStatementCount(t *testing.T) {
	ft := &fakeTx{}
	tx := newPgTransaction(ft, nil)
	require.Equal(t, 0, tx.StatementCount())

	_ = tx.Savepoint(context.Background(), "a")
	_ = tx.Release(context.Background(), "a")
	_ = tx.Savepoint(context.Background(), "b")
	_ = tx.RollbackTo(context.Background(), "b")
	require.Equal(t, 4, tx.StatementCount())
}

func TestTransactionSavepointsReturnsCopy(t *testing.T) {
	ft := &fakeTx{}
	tx := newPgTransaction(ft, nil)
	_ = tx.Savepoint(context.Background(), "sp1")

	sp := tx.Savepoints()
	sp[0] = "mutated"
	require.Equal(t, []string{"sp1"}, tx.Savepoints(), "mutation of returned slice must not affect internal state")
}

func TestSessionInTransactionFalseForNewSession(t *testing.T) {
	s := newStubSession()
	require.False(t, s.InTransaction())
	require.Nil(t, s.CurrentTransaction())
}

func TestReview_txBeginRejectsExistingOwnership(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  byte
		api  models.TxStatus
	}{
		{name: "raw active", raw: 'T'},
		{name: "raw aborted", raw: 'E'},
		{name: "API active", api: models.TxActive},
		{name: "API aborted", api: models.TxAbortedInTx},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStubSession()
			s.cachedTxStatus.Store(uint32(tc.raw))
			if tc.api != "" {
				s.activeTx = newPgTransaction(&fakeTx{}, nil)
				s.activeTx.status = tc.api
			} else {
				require.False(t, s.InTransaction(), "raw status does not confer API ownership")
				require.Nil(t, s.CurrentTransaction())
			}
			before := s.activeTx
			require.NotPanics(t, func() {
				tx, err := s.Begin(context.Background(), models.TxOptions{})
				require.ErrorContains(t, err, "transaction already in progress")
				require.Nil(t, tx)
			})
			require.True(t, before == s.activeTx)
			require.Equal(t, tc.raw, byte(s.cachedTxStatus.Load()))
		})
	}
}
