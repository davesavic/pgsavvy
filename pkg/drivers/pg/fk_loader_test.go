package pg

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/pgsavvy/pkg/models"
)

// fakeFKRows is a minimal pgx.Rows test double for scanForeignKeys. It
// replays a pre-staged sequence of rows (each row is the positional arg list
// for the Scan call) and an optional terminal error surfaced by Err(). Only
// Next/Scan/Err/Close are exercised by scanForeignKeys; the other pgx.Rows
// methods return zero values and exist solely to satisfy the interface.
type fakeFKRows struct {
	rows   [][]any
	idx    int
	err    error
	closed bool
}

func (r *fakeFKRows) Close()                        { r.closed = true }
func (r *fakeFKRows) Err() error                    { return r.err }
func (r *fakeFKRows) CommandTag() pgconn.CommandTag { return pgconn.CommandTag{} }
func (r *fakeFKRows) FieldDescriptions() []pgconn.FieldDescription {
	return nil
}

func (r *fakeFKRows) Next() bool {
	if r.idx >= len(r.rows) {
		return false
	}
	r.idx++
	return true
}

func (r *fakeFKRows) Scan(dest ...any) error {
	row := r.rows[r.idx-1]
	if len(dest) != len(row) {
		return errors.New("fakeFKRows: dest/src length mismatch")
	}
	for i := range dest {
		switch d := dest[i].(type) {
		case *string:
			*d = row[i].(string)
		case *[]string:
			*d = row[i].([]string)
		default:
			return errors.New("fakeFKRows: unsupported dest type")
		}
	}
	return nil
}

func (r *fakeFKRows) Values() ([]any, error) { return nil, nil }
func (r *fakeFKRows) RawValues() [][]byte    { return nil }
func (r *fakeFKRows) Conn() *pgx.Conn        { return nil }

// Compile-time assertion that fakeFKRows satisfies pgx.Rows. Drift on either
// side fails the package build.
var _ pgx.Rows = (*fakeFKRows)(nil)

func TestScanForeignKeys_EmptyReturnsNonNilSlice(t *testing.T) {
	rows := &fakeFKRows{}
	out, err := scanForeignKeys(rows)
	require.NoError(t, err)
	require.NotNil(t, out, "scanForeignKeys must return non-nil slice on zero rows")
	require.Len(t, out, 0)
}

func TestScanForeignKeys_SingleSimpleFK(t *testing.T) {
	rows := &fakeFKRows{
		rows: [][]any{
			{
				"posts_user_id_fkey", // name
				"app",                // schema
				"posts",              // table
				[]string{"user_id"},  // columns
				"app",                // ref_schema
				"users",              // ref_table
				[]string{"id"},       // ref_columns
				"NO ACTION",          // on_update
				"CASCADE",            // on_delete
			},
		},
	}
	out, err := scanForeignKeys(rows)
	require.NoError(t, err)
	require.Equal(t, []models.ForeignKey{
		{
			Name:       "posts_user_id_fkey",
			Schema:     "app",
			Table:      "posts",
			Columns:    []string{"user_id"},
			RefSchema:  "app",
			RefTable:   "users",
			RefColumns: []string{"id"},
			OnUpdate:   "NO ACTION",
			OnDelete:   "CASCADE",
		},
	}, out)
}

func TestScanForeignKeys_CompositeFKPreservesOrder(t *testing.T) {
	rows := &fakeFKRows{
		rows: [][]any{
			{
				"child_composite_fkey",
				"app",
				"child",
				[]string{"a_id", "b_id"},
				"app",
				"parent",
				[]string{"a", "b"},
				"NO ACTION",
				"NO ACTION",
			},
		},
	}
	out, err := scanForeignKeys(rows)
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Equal(t, []string{"a_id", "b_id"}, out[0].Columns)
	require.Equal(t, []string{"a", "b"}, out[0].RefColumns)
}

func TestScanForeignKeys_PropagatesRowsErr(t *testing.T) {
	rows := &fakeFKRows{err: errors.New("simulated rows.Err")}
	_, err := scanForeignKeys(rows)
	require.Error(t, err)
}
