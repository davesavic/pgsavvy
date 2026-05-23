package pg

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

// fakeFuncRows is a minimal pgx.Rows test double for scanFunctionNames.
// It replays a pre-staged sequence of single-string rows and an optional
// terminal error surfaced by Err(). Only Next/Scan/Err/Close are
// exercised by scanFunctionNames; the other pgx.Rows methods return zero
// values and exist solely to satisfy the interface.
type fakeFuncRows struct {
	rows   []string
	idx    int
	err    error
	closed bool
}

func (r *fakeFuncRows) Close()                        { r.closed = true }
func (r *fakeFuncRows) Err() error                    { return r.err }
func (r *fakeFuncRows) CommandTag() pgconn.CommandTag { return pgconn.CommandTag{} }
func (r *fakeFuncRows) FieldDescriptions() []pgconn.FieldDescription {
	return nil
}

func (r *fakeFuncRows) Next() bool {
	if r.idx >= len(r.rows) {
		return false
	}
	r.idx++
	return true
}

func (r *fakeFuncRows) Scan(dest ...any) error {
	if len(dest) != 1 {
		return errors.New("fakeFuncRows: expected single dest")
	}
	d, ok := dest[0].(*string)
	if !ok {
		return errors.New("fakeFuncRows: dest must be *string")
	}
	*d = r.rows[r.idx-1]
	return nil
}

func (r *fakeFuncRows) Values() ([]any, error) { return nil, nil }
func (r *fakeFuncRows) RawValues() [][]byte    { return nil }
func (r *fakeFuncRows) Conn() *pgx.Conn        { return nil }

// Compile-time assertion that fakeFuncRows satisfies pgx.Rows. Drift on
// either side fails the package build.
var _ pgx.Rows = (*fakeFuncRows)(nil)

func TestScanFunctionNames_EmptyReturnsNonNilSlice(t *testing.T) {
	rows := &fakeFuncRows{}
	out, err := scanFunctionNames(rows)
	require.NoError(t, err)
	require.NotNil(t, out, "scanFunctionNames must return non-nil slice on zero rows")
	require.Len(t, out, 0)
}

func TestScanFunctionNames_MultipleRows(t *testing.T) {
	rows := &fakeFuncRows{rows: []string{"add_one", "compute_total", "do_thing"}}
	out, err := scanFunctionNames(rows)
	require.NoError(t, err)
	require.Equal(t, []string{"add_one", "compute_total", "do_thing"}, out)
}

func TestScanFunctionNames_PropagatesRowsErr(t *testing.T) {
	rows := &fakeFuncRows{err: errors.New("simulated rows.Err")}
	_, err := scanFunctionNames(rows)
	require.Error(t, err)
}
