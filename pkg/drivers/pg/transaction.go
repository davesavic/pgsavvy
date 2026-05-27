package pg

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// commitRollbackDeadline is the maximum time Commit/Rollback will wait before
// giving up. Prevents hanging indefinitely on dead connections (AD-7).
const commitRollbackDeadline = 5 * time.Second

// pgTransaction wraps a pgx.Tx with savepoint tracking and lifecycle state.
// It implements drivers.Transaction.
type pgTransaction struct {
	tx         pgx.Tx
	status     models.TxStatus
	savepoints []string
	stmtCount  atomic.Int64
}

func newPgTransaction(tx pgx.Tx) *pgTransaction {
	return &pgTransaction{
		tx:     tx,
		status: models.TxActive,
	}
}

func (t *pgTransaction) Commit(ctx context.Context) error {
	if t.status != models.TxActive {
		return fmt.Errorf("transaction: cannot commit in state %s", t.status)
	}
	ctx, cancel := context.WithTimeout(ctx, commitRollbackDeadline)
	defer cancel()
	if err := t.tx.Commit(ctx); err != nil {
		return err
	}
	t.status = models.TxCommitted
	return nil
}

func (t *pgTransaction) Rollback(ctx context.Context) error {
	if t.status != models.TxActive && t.status != models.TxAbortedInTx {
		return fmt.Errorf("transaction: cannot rollback in state %s", t.status)
	}
	ctx, cancel := context.WithTimeout(ctx, commitRollbackDeadline)
	defer cancel()
	if err := t.tx.Rollback(ctx); err != nil {
		return err
	}
	t.status = models.TxRolledBack
	return nil
}

func (t *pgTransaction) Savepoint(ctx context.Context, name string) error {
	if name == "" {
		return errors.New("transaction: savepoint name must not be empty")
	}
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err := t.tx.Exec(ctx, "SAVEPOINT "+quoted); err != nil {
		return err
	}
	t.savepoints = append(t.savepoints, name)
	t.stmtCount.Add(1)
	return nil
}

func (t *pgTransaction) Release(ctx context.Context, name string) error {
	if name == "" {
		return errors.New("transaction: savepoint name must not be empty")
	}
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err := t.tx.Exec(ctx, "RELEASE SAVEPOINT "+quoted); err != nil {
		return err
	}
	t.savepoints = slices.DeleteFunc(t.savepoints, func(s string) bool { return s == name })
	t.stmtCount.Add(1)
	return nil
}

func (t *pgTransaction) RollbackTo(ctx context.Context, name string) error {
	if name == "" {
		return errors.New("transaction: savepoint name must not be empty")
	}
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err := t.tx.Exec(ctx, "ROLLBACK TO SAVEPOINT "+quoted); err != nil {
		return err
	}
	t.stmtCount.Add(1)
	return nil
}

func (t *pgTransaction) Savepoints() []string {
	out := make([]string, len(t.savepoints))
	copy(out, t.savepoints)
	return out
}

func (t *pgTransaction) Status() models.TxStatus {
	return t.status
}

// ObserveError transitions the transaction to TxAbortedInTx when err wraps a
// pgconn.PgError with SQLSTATE 25P02 (in_failed_sql_transaction). All other
// errors are ignored — the caller decides whether to surface them.
func (t *pgTransaction) ObserveError(err error) {
	if err == nil {
		return
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "25P02" {
		t.status = models.TxAbortedInTx
	}
}

func (t *pgTransaction) StatementCount() int {
	return int(t.stmtCount.Load())
}

var _ drivers.Transaction = (*pgTransaction)(nil)
