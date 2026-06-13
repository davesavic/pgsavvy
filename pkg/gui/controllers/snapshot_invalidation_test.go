package controllers

import (
	"errors"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"
	"github.com/stretchr/testify/require"
)

// invSchemaPicker is a minimal SchemaPicker returning a fixed selected schema.
type invSchemaPicker struct{ name string }

func (p invSchemaPicker) SelectedSchemaName() string { return p.name }
func (p invSchemaPicker) ToggleShowHidden()          {}

// fakeMetaInvalidator records InvalidateSchema / InvalidateTable calls so the
// post-run DDL gate's behaviour is asserted without a live SchemaWarmer.
type fakeMetaInvalidator struct {
	schemas []string
	tables  [][2]string
}

func (f *fakeMetaInvalidator) InvalidateSchema(schema string) {
	f.schemas = append(f.schemas, schema)
}

func (f *fakeMetaInvalidator) InvalidateTable(schema, table string) {
	f.tables = append(f.tables, [2]string{schema, table})
}

var _ SchemaMetadataInvalidator = (*fakeMetaInvalidator)(nil)

// closedDone returns an already-closed done channel so the inline OnWorker in
// the test bag (runs the closure synchronously) drains it immediately.
func closedDone() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// newQEC builds a QueryEditorController with a fake metadata invalidator, a
// fixed selected schema, and a SYNCHRONOUS OnWorker (runs its closure inline)
// so scheduleDDLInvalidation completes deterministically within the call.
func newQEC(t *testing.T, schema string) (*QueryEditorController, *fakeMetaInvalidator) {
	t.Helper()
	inv := &fakeMetaInvalidator{}
	nav := NavDeps{Schemas: invSchemaPicker{name: schema}}
	query := QueryDeps{MetadataInvalidator: inv}
	threading := ThreadingDeps{
		OnWorker: func(fn func(gocui.Task) error) {
			if fn != nil {
				_ = fn(nil)
			}
		},
	}
	ctrl := NewQueryEditorController(nil, CoreDeps{}, nav, UIDeps{}, query, threading)
	return ctrl, inv
}

// TestSnapshotInvalidation_PostRunDDLSuccess: a successful local DDL invalidates
// the active schema (whole-schema, epic decision B), fired from the post-run
// signal (done channel), not the pre-run gate.
func TestSnapshotInvalidation_PostRunDDLSuccess(t *testing.T) {
	ctrl, inv := newQEC(t, "public")

	ctrl.scheduleDDLInvalidation(
		"ALTER TABLE users ADD COLUMN nickname text",
		closedDone(),
		func() error { return nil },
	)

	require.Equal(t, []string{"public"}, inv.schemas, "successful DDL invalidates the active schema")
}

// TestSnapshotInvalidation_PostRunDDLError: a FAILED DDL must NOT invalidate —
// the on-disk shape did not change.
func TestSnapshotInvalidation_PostRunDDLError(t *testing.T) {
	ctrl, inv := newQEC(t, "public")

	ctrl.scheduleDDLInvalidation(
		"ALTER TABLE users ADD COLUMN nickname text",
		closedDone(),
		func() error { return errors.New("syntax error") },
	)

	require.Empty(t, inv.schemas, "failed DDL must not invalidate")
}

// TestSnapshotInvalidation_PostRunNonDDLNoInvalidate: a plain SELECT/DML and a
// writable-CTE DML (EffectiveKind == KindDML) must NOT invalidate.
func TestSnapshotInvalidation_PostRunNonDDLNoInvalidate(t *testing.T) {
	cases := []struct {
		name string
		sql  string
	}{
		{"plain select", "SELECT * FROM users"},
		{"plain dml insert", "INSERT INTO users (name) VALUES ('x')"},
		{"plain dml update", "UPDATE users SET name='x' WHERE id=1"},
		{"writable cte insert", "WITH n AS (INSERT INTO logs VALUES (1) RETURNING id) SELECT * FROM n"},
		{"writable cte delete", "WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl, inv := newQEC(t, "public")
			ctrl.scheduleDDLInvalidation(tc.sql, closedDone(), func() error { return nil })
			require.Empty(t, inv.schemas, "non-DDL must not invalidate")
		})
	}
}

// TestSnapshotInvalidation_PostRunDDLNoSchemaNoInvalidate: with no selected
// schema the whole-schema invalidation has no target and is skipped.
func TestSnapshotInvalidation_PostRunDDLNoSchemaNoInvalidate(t *testing.T) {
	ctrl, inv := newQEC(t, "")
	ctrl.scheduleDDLInvalidation("DROP TABLE users", closedDone(), func() error { return nil })
	require.Empty(t, inv.schemas, "no selected schema -> no invalidation target")
}

// TestSnapshotInvalidation_PostRunDDLDropRemovesEntry documents that DROP TABLE
// (KindDDL) invalidates the schema scope just like ALTER/CREATE — the dropped
// table's entry is removed by the whole-schema sweep, and a later CREATE of the
// same name re-warms fresh (the re-warm is the store's job, verified in the
// data package test).
func TestSnapshotInvalidation_PostRunDDLDropRemovesEntry(t *testing.T) {
	ctrl, inv := newQEC(t, "public")
	ctrl.scheduleDDLInvalidation("DROP TABLE users", closedDone(), func() error { return nil })
	require.Equal(t, []string{"public"}, inv.schemas)
}
