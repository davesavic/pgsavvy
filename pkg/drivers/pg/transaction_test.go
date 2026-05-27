package pg

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/dbsavvy/pkg/models"
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
func (f *fakeTx) Rollback(_ context.Context) error  { return f.rollbackErr }
func (f *fakeTx) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	f.execCalls = append(f.execCalls, sql)
	return pgconn.NewCommandTag(""), f.execErr
}

func TestTransactionCommit(t *testing.T) {
	ft := &fakeTx{}
	tx := newPgTransaction(ft)
	require.Equal(t, models.TxActive, tx.Status())

	err := tx.Commit(context.Background())
	require.NoError(t, err)
	require.Equal(t, models.TxCommitted, tx.Status())
}

func TestTransactionCommitRejectsNonActive(t *testing.T) {
	ft := &fakeTx{}
	tx := newPgTransaction(ft)
	tx.status = models.TxCommitted
	err := tx.Commit(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot commit")
}

func TestTransactionRollback(t *testing.T) {
	ft := &fakeTx{}
	tx := newPgTransaction(ft)

	err := tx.Rollback(context.Background())
	require.NoError(t, err)
	require.Equal(t, models.TxRolledBack, tx.Status())
}

func TestTransactionRollbackFromAbortedState(t *testing.T) {
	ft := &fakeTx{}
	tx := newPgTransaction(ft)
	tx.status = models.TxAbortedInTx

	err := tx.Rollback(context.Background())
	require.NoError(t, err)
	require.Equal(t, models.TxRolledBack, tx.Status())
}

func TestTransactionRollbackRejectsCommitted(t *testing.T) {
	ft := &fakeTx{}
	tx := newPgTransaction(ft)
	tx.status = models.TxCommitted

	err := tx.Rollback(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot rollback")
}

func TestTransactionSavepoint(t *testing.T) {
	ft := &fakeTx{}
	tx := newPgTransaction(ft)

	err := tx.Savepoint(context.Background(), "sp1")
	require.NoError(t, err)
	require.Equal(t, []string{"sp1"}, tx.Savepoints())
	require.Len(t, ft.execCalls, 1)
	require.Contains(t, ft.execCalls[0], "SAVEPOINT")
	require.Equal(t, 1, tx.StatementCount())
}

func TestTransactionSavepointEmptyNameReturnsError(t *testing.T) {
	ft := &fakeTx{}
	tx := newPgTransaction(ft)

	err := tx.Savepoint(context.Background(), "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "must not be empty")
}

func TestTransactionSavepointInjectionPayloadSafelyQuoted(t *testing.T) {
	ft := &fakeTx{}
	tx := newPgTransaction(ft)

	err := tx.Savepoint(context.Background(), "sp1; DROP TABLE users; --")
	require.NoError(t, err)
	require.Len(t, ft.execCalls, 1)
	// pgx.Identifier.Sanitize() double-quotes the name, making the injection
	// payload a harmless identifier rather than executable SQL.
	assert.Equal(t, `SAVEPOINT "sp1; DROP TABLE users; --"`, ft.execCalls[0])
}

func TestTransactionRelease(t *testing.T) {
	ft := &fakeTx{}
	tx := newPgTransaction(ft)

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
	tx := newPgTransaction(ft)
	err := tx.Release(context.Background(), "")
	require.Error(t, err)
}

func TestTransactionRollbackTo(t *testing.T) {
	ft := &fakeTx{}
	tx := newPgTransaction(ft)

	require.NoError(t, tx.Savepoint(context.Background(), "sp1"))

	err := tx.RollbackTo(context.Background(), "sp1")
	require.NoError(t, err)
	require.Contains(t, ft.execCalls[1], "ROLLBACK TO SAVEPOINT")
}

func TestTransactionRollbackToEmptyNameReturnsError(t *testing.T) {
	ft := &fakeTx{}
	tx := newPgTransaction(ft)
	err := tx.RollbackTo(context.Background(), "")
	require.Error(t, err)
}

func TestTransactionObserveError25P02(t *testing.T) {
	ft := &fakeTx{}
	tx := newPgTransaction(ft)
	require.Equal(t, models.TxActive, tx.Status())

	tx.ObserveError(&pgconn.PgError{Code: "25P02"})
	require.Equal(t, models.TxAbortedInTx, tx.Status())
}

func TestTransactionObserveErrorNon25P02IsNoop(t *testing.T) {
	ft := &fakeTx{}
	tx := newPgTransaction(ft)

	tx.ObserveError(&pgconn.PgError{Code: "42P01"})
	require.Equal(t, models.TxActive, tx.Status())
}

func TestTransactionObserveErrorNilIsNoop(t *testing.T) {
	ft := &fakeTx{}
	tx := newPgTransaction(ft)
	tx.ObserveError(nil)
	require.Equal(t, models.TxActive, tx.Status())
}

func TestTransactionObserveErrorPlainErrorIsNoop(t *testing.T) {
	ft := &fakeTx{}
	tx := newPgTransaction(ft)
	tx.ObserveError(errors.New("something"))
	require.Equal(t, models.TxActive, tx.Status())
}

func TestTransactionStatementCount(t *testing.T) {
	ft := &fakeTx{}
	tx := newPgTransaction(ft)
	require.Equal(t, 0, tx.StatementCount())

	_ = tx.Savepoint(context.Background(), "a")
	_ = tx.Release(context.Background(), "a")
	_ = tx.Savepoint(context.Background(), "b")
	_ = tx.RollbackTo(context.Background(), "b")
	require.Equal(t, 4, tx.StatementCount())
}

func TestTransactionSavepointsReturnsCopy(t *testing.T) {
	ft := &fakeTx{}
	tx := newPgTransaction(ft)
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
