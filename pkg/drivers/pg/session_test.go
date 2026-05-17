package pg

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// Compile-time assertion that *Session satisfies drivers.Session. A signature
// drift on either side breaks the package build before any test runs.
var _ drivers.Session = (*Session)(nil)

// newStubSession returns a Session that has a non-nil parent but a nil
// pgxpool.Conn. Safe to use only with methods that never touch s.conn
// (ErrNotImplemented stubs, InTransaction, CurrentTransaction).
func newStubSession() *Session {
	return &Session{
		id:     models.SessionID(sessionIDCounter.Add(1)),
		parent: &Connection{},
	}
}

func TestSessionImplementsDriversSession(t *testing.T) {
	// Pin the interface assertion inside the test binary so it can't be
	// dead-code-eliminated.
	var s drivers.Session = newStubSession()
	require.NotNil(t, s)
}

func TestSessionIDIsMonotonic(t *testing.T) {
	a := newStubSession()
	b := newStubSession()
	require.NotZero(t, a.ID())
	require.NotZero(t, b.ID())
	require.Greater(t, b.ID(), a.ID(), "second Session ID must be greater than the first")
}

func TestSessionExecuteReturnsErrNotImplemented(t *testing.T) {
	s := newStubSession()
	res, err := s.Execute(context.Background(), models.Query{SQL: "SELECT 1"})
	require.ErrorIs(t, err, drivers.ErrNotImplemented)
	require.Equal(t, models.Result{}, res)
}

func TestSessionStreamReturnsUntypedNilOnErrNotImplemented(t *testing.T) {
	s := newStubSession()
	stream, err := s.Stream(context.Background(), models.Query{SQL: "SELECT 1"})
	require.ErrorIs(t, err, drivers.ErrNotImplemented)
	require.Nil(t, stream)
	require.True(t, stream == nil, "RowStream must be untyped nil, not a typed nil interface")
}

func TestSessionExplainReturnsErrNotImplemented(t *testing.T) {
	s := newStubSession()
	plan, err := s.Explain(context.Background(), models.Query{SQL: "SELECT 1"}, false)
	require.ErrorIs(t, err, drivers.ErrNotImplemented)
	require.Equal(t, models.Plan{}, plan)
}

func TestSessionBeginReturnsUntypedNilOnErrNotImplemented(t *testing.T) {
	s := newStubSession()
	tx, err := s.Begin(context.Background(), models.TxOptions{})
	require.Nil(t, tx)
	require.True(t, tx == nil, "Transaction must be untyped nil, not a typed nil interface")
	require.ErrorIs(t, err, drivers.ErrNotImplemented)
}

func TestSessionDescribeFunctionReturnsErrNotImplemented(t *testing.T) {
	s := newStubSession()
	fd, err := s.DescribeFunction(context.Background(), "public", "foo")
	require.ErrorIs(t, err, drivers.ErrNotImplemented)
	require.Equal(t, models.FunctionDetail{}, fd)
}

func TestSessionInTransactionFalseCurrentTransactionNil(t *testing.T) {
	s := newStubSession()
	require.False(t, s.InTransaction())
	require.Nil(t, s.CurrentTransaction())
}

func TestSessionConcurrentUsePanics(t *testing.T) {
	// Synthetic CAS preconditioning makes the panic deterministic without a
	// live pool: hold the inFlight flag manually, then call any guarded
	// method. The guard's CAS(0,1) MUST fail and panic.
	s := newStubSession()
	s.inFlight.Store(1)
	require.PanicsWithValue(t, "session: concurrent use", func() {
		_, _ = s.DescribeFunction(context.Background(), "x", "y")
	})
}

func TestSessionUseAfterClosePanics(t *testing.T) {
	s := newStubSession()
	s.closed.Store(true)
	require.PanicsWithValue(t, "session: use after Close", func() {
		_, _ = s.DescribeFunction(context.Background(), "x", "y")
	})
}

func TestWrapPgErrorReturnsQueryError(t *testing.T) {
	src := &pgconn.PgError{
		Code:           "42P01",
		Severity:       "ERROR",
		Message:        "relation does not exist",
		Hint:           "did you forget to migrate?",
		Detail:         "no such relation",
		Where:          "parser",
		SchemaName:     "public",
		TableName:      "missing",
		ColumnName:     "id",
		ConstraintName: "pk_missing",
		Position:       17,
	}
	out := wrapPgError(src)
	require.NotNil(t, out)
	var qe *drivers.QueryError
	require.True(t, errors.As(out, &qe), "wrapPgError should yield *drivers.QueryError")
	require.Equal(t, "42P01", qe.Code)
	require.Equal(t, "ERROR", qe.Severity)
	require.Equal(t, "did you forget to migrate?", qe.Hint)
	require.Equal(t, "no such relation", qe.Detail)
	require.Equal(t, "parser", qe.Where)
	require.Equal(t, "public", qe.Schema)
	require.Equal(t, "missing", qe.Table)
	require.Equal(t, "id", qe.Column)
	require.Equal(t, "pk_missing", qe.Constraint)
	require.Equal(t, 17, qe.Position)
	require.ErrorIs(t, out, src)
}

func TestWrapPgErrorPassesThroughNonPgError(t *testing.T) {
	plain := errors.New("plain")
	require.Same(t, errInterface(plain), errInterface(wrapPgError(plain)))
	require.Nil(t, wrapPgError(nil))
}

// errInterface returns the underlying pointer of an error so require.Same can
// compare identity across the error interface boundary.
func errInterface(e error) error { return e }

func TestParseMajorVersion(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"PostgreSQL 17.4 on x86_64-pc-linux-gnu, compiled by gcc", 17},
		{"PostgreSQL 18.0 on aarch64-apple-darwin", 18},
		{"PostgreSQL 9.6.24 on i686-linux-gnu", 9},
		{"", 0},
		{"garbage", 0},
		{"PostgreSQL ", 0},
		{"PostgreSQL abc", 0},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			require.Equal(t, tc.want, parseMajorVersion(tc.in))
		})
	}
}

func TestWarnIfPostgresGE18OnceWithModernVersion(t *testing.T) {
	out := captureStderr(t, func() {
		c := &Connection{majorVersion: 18}
		c.warnIfPostgresGE18()
		c.warnIfPostgresGE18()
		c.warnIfPostgresGE18()
	})
	occurrences := strings.Count(out, "WARN: pg: server reports PostgreSQL 18")
	require.Equal(t, 1, occurrences, "expected exactly one WARN line; got %d in: %q", occurrences, out)
}

func TestWarnIfPostgresGE18SilentForOlder(t *testing.T) {
	out := captureStderr(t, func() {
		c := &Connection{majorVersion: 17}
		c.warnIfPostgresGE18()
		c.warnIfPostgresGE18()
		c.warnIfPostgresGE18()
	})
	require.Empty(t, out, "no warning expected for Postgres < 18; got: %q", out)
}

func TestFindTableHelpers(t *testing.T) {
	a := &models.Table{Schema: "public", Name: "users"}
	b := &models.Table{Schema: "app", Name: "orders"}
	tables := []*models.Table{a, b}

	require.Same(t, a, findTable(tables, "public", "users"))
	require.Same(t, b, findTable(tables, "app", "orders"))
	require.Nil(t, findTable(tables, "public", "missing"))
	require.Nil(t, findTable(nil, "public", "users"))

	require.Equal(t, []string{"public", "app"}, schemaList(tables))
	require.Equal(t, []string{"users", "orders"}, nameList(tables))
}

// captureStderr swaps os.Stderr with a pipe, runs fn, then restores stderr
// and returns everything written.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stderr
	os.Stderr = w
	done := make(chan string, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	defer func() {
		os.Stderr = orig
	}()

	fn()
	_ = w.Close()
	wg.Wait()
	return <-done
}
