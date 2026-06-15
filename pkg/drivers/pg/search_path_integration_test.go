//go:build integration

// Integration test for Session.Stream honoring Query.DefaultSchema by issuing
// SET search_path before the statement. Mirrors the
// openIntegrationSession pattern from editability_integration_test.go. Skipped
// (not failed) when DBSAVVY_TEST_PG is unset.

package pg_test

import (
	"context"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/drivers/pg"
	"github.com/davesavic/pgsavvy/pkg/models"
)

func spExec(t *testing.T, sess *pg.Session, sql string) {
	t.Helper()
	if _, err := sess.Execute(context.Background(), models.Query{SQL: sql}); err != nil {
		t.Fatalf("Execute(%q): %v", sql, err)
	}
}

// spFirstLabel streams sql with the given DefaultSchema and returns the first
// row's single text column.
func spFirstLabel(t *testing.T, sess *pg.Session, sql, defaultSchema string) string {
	t.Helper()
	stream, err := sess.Stream(context.Background(), models.Query{SQL: sql, DefaultSchema: defaultSchema})
	if err != nil {
		t.Fatalf("Stream(%q, schema=%q): %v", sql, defaultSchema, err)
	}
	defer func() { _ = stream.Close() }()
	row, ok, err := stream.Next(context.Background())
	if err != nil {
		t.Fatalf("Next(%q, schema=%q): %v", sql, defaultSchema, err)
	}
	if !ok {
		t.Fatalf("no rows for %q (schema=%q)", sql, defaultSchema)
	}
	if len(row.Values) != 1 {
		t.Fatalf("row has %d values, want 1", len(row.Values))
	}
	s, ok := row.Values[0].(string)
	if !ok {
		t.Fatalf("value type %T, want string", row.Values[0])
	}
	return s
}

func TestStream_DefaultSchemaResolvesUnqualifiedName(t *testing.T) {
	sess := openIntegrationSession(t)
	ctx := context.Background()

	schemas := []string{"sp_probe_a", "sp_probe_b"}
	for _, s := range schemas {
		spExec(t, sess, "DROP SCHEMA IF EXISTS "+s+" CASCADE")
		spExec(t, sess, "CREATE SCHEMA "+s)
	}
	t.Cleanup(func() {
		for _, s := range schemas {
			_, _ = sess.Execute(ctx, models.Query{SQL: "DROP SCHEMA IF EXISTS " + s + " CASCADE"})
		}
	})

	// Same table name in both schemas, distinct contents.
	spExec(t, sess, "CREATE TABLE sp_probe_a.probe (label text)")
	spExec(t, sess, "INSERT INTO sp_probe_a.probe (label) VALUES ('from_a')")
	spExec(t, sess, "CREATE TABLE sp_probe_b.probe (label text)")
	spExec(t, sess, "INSERT INTO sp_probe_b.probe (label) VALUES ('from_b')")

	// Unqualified name resolves against whichever schema is "selected".
	if got := spFirstLabel(t, sess, "SELECT label FROM probe", "sp_probe_a"); got != "from_a" {
		t.Errorf("unqualified probe with DefaultSchema=sp_probe_a = %q, want from_a", got)
	}
	if got := spFirstLabel(t, sess, "SELECT label FROM probe", "sp_probe_b"); got != "from_b" {
		t.Errorf("unqualified probe with DefaultSchema=sp_probe_b = %q, want from_b", got)
	}

	// A qualified name still wins over the selected schema (search_path
	// semantics: explicit qualification is never overridden).
	if got := spFirstLabel(t, sess, "SELECT label FROM sp_probe_b.probe", "sp_probe_a"); got != "from_b" {
		t.Errorf("qualified sp_probe_b.probe with DefaultSchema=sp_probe_a = %q, want from_b", got)
	}
}

func TestExplain_DefaultSchemaResolvesUnqualifiedName(t *testing.T) {
	sess := openIntegrationSession(t)
	ctx := context.Background()

	spExec(t, sess, "DROP SCHEMA IF EXISTS sp_explain CASCADE")
	spExec(t, sess, "CREATE SCHEMA sp_explain")
	t.Cleanup(func() {
		_, _ = sess.Execute(ctx, models.Query{SQL: "DROP SCHEMA IF EXISTS sp_explain CASCADE"})
	})
	// probe lives ONLY in sp_explain — never on the default search_path.
	spExec(t, sess, "CREATE TABLE sp_explain.probe (label text)")

	// Fresh session: without a default schema the unqualified name is
	// unresolvable, so EXPLAIN errors. Run this branch FIRST, before any
	// SET search_path has touched the pinned connection.
	if _, err := sess.Explain(ctx, models.Query{SQL: "SELECT label FROM probe"}, false); err == nil {
		t.Fatalf("expected EXPLAIN of unqualified probe to fail with no default schema")
	}

	// With the schema, search_path is set first and EXPLAIN resolves it.
	plan, err := sess.Explain(ctx, models.Query{SQL: "SELECT label FROM probe", DefaultSchema: "sp_explain"}, false)
	if err != nil {
		t.Fatalf("EXPLAIN with DefaultSchema=sp_explain: %v", err)
	}
	if plan.RawText == "" {
		t.Fatalf("expected a non-empty plan, got empty RawText")
	}
}
