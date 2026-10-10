//go:build integration

package pg_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/drivers/pg"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers"
	"github.com/davesavic/pgsavvy/pkg/models"
)

func TestStreamFreshRelationMetadataAcrossSchemas(t *testing.T) {
	dsn := os.Getenv("PGSAVVY_TEST_PG")
	if dsn == "" {
		t.Skip("PGSAVVY_TEST_PG unset")
	}
	for _, mode := range []string{"cache_statement", "cache_describe", "describe_exec", "exec", "simple_protocol"} {
		t.Run(mode, func(t *testing.T) {
			parsed, err := url.Parse(dsn)
			if err != nil || parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
				t.Fatal("metadata mode regression requires a PostgreSQL URI fixture")
			}
			params := parsed.Query()
			params.Set("default_query_exec_mode", mode)
			parsed.RawQuery = params.Encode()
			t.Setenv("PGSAVVY_TEST_PG", parsed.String())
			testFreshRelationMetadataAcrossSchemas(t)
		})
	}
}

func testFreshRelationMetadataAcrossSchemas(t *testing.T) {
	t.Helper()
	sess := openIntegrationSession(t)
	ctx := context.Background()
	base := fmt.Sprintf("review_metadata_%d", time.Now().UnixNano())
	schemas := []string{base + "_a", base + "_b"}
	for _, schema := range schemas {
		spExec(t, sess, "CREATE SCHEMA "+pg.QuoteIdent(schema))
		spExec(t, sess, "CREATE TABLE "+pg.QuoteQualified(schema, "items")+" (id integer PRIMARY KEY, value text)")
		spExec(t, sess, "INSERT INTO "+pg.QuoteQualified(schema, "items")+" VALUES (1, 'old'), (2, 'untouched')")
		t.Cleanup(func() {
			_, err := sess.Execute(ctx, models.Query{SQL: "DROP SCHEMA " + pg.QuoteIdent(schema) + " CASCADE"})
			if err != nil {
				t.Errorf("owned schema cleanup: %v", err)
			}
		})
	}
	var previousOID uint32
	for _, schema := range []string{schemas[0], schemas[1], schemas[0], schemas[1]} {
		stream, err := sess.Stream(ctx, models.Query{SQL: "SELECT id, value FROM items WHERE id >= $1 ORDER BY id", Args: []any{1}, DefaultSchema: schema})
		if err != nil {
			t.Fatal(err)
		}
		cols := stream.Columns()
		row, ok, err := stream.Next(ctx)
		if err != nil || !ok || len(row.Values) != 2 || row.Values[0] != int32(1) {
			_ = stream.Close()
			t.Fatalf("parameterized stream lost its bound row: %v", err)
		}
		if err := stream.Close(); err != nil {
			t.Fatal(err)
		}
		ref, rowID, reason, err := pg.EditabilityIntrospect(ctx, sess, cols)
		if err != nil || reason != "" || len(rowID) != 1 || ref.Schema != schema || ref.Table != "items" {
			t.Fatalf("selected schema %q resolved target %v, identity=%v reason=%q err=%v", schema, ref, rowID, reason, err)
		}
		if cols[0].TableOID == previousOID {
			t.Fatal("schema switch retained the prior relation OID")
		}
		previousOID = cols[0].TableOID
		if schema == schemas[1] {
			batch, err := helpers.BuildUpdateBatch(ref, []string{"id"}, []models.PendingEdit{{PrimaryKey: []any{1}, Column: "value", OldValue: "old", NewValue: "changed"}}, helpers.SQLModeParameterized, true)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := sess.Execute(ctx, models.Query{SQL: batch[0].SQL, Args: batch[0].Args}); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i, schema := range schemas {
		result, err := sess.Execute(ctx, models.Query{SQL: "SELECT value FROM " + pg.QuoteQualified(schema, "items") + " ORDER BY id"})
		want := "old"
		if i == 1 {
			want = "changed"
		}
		if err != nil || len(result.Rows) != 2 || result.Rows[0].Values[0] != want || result.Rows[1].Values[0] != "untouched" {
			t.Fatalf("wrong mutation target or unrelated row changed in %q: %v", schema, err)
		}
	}
}

func TestStreamSelectedSchemaAbortedTransactionRecovery(t *testing.T) {
	for _, recovery := range []string{"ROLLBACK", "COMMIT", "ROLLBACK TO SAVEPOINT retained"} {
		t.Run(recovery, func(t *testing.T) {
			sess := openIntegrationSession(t)
			ctx := context.Background()
			schema := fmt.Sprintf("review_recovery_%d", time.Now().UnixNano())
			spExec(t, sess, "CREATE SCHEMA "+pg.QuoteIdent(schema))
			spExec(t, sess, "CREATE TABLE "+pg.QuoteQualified(schema, "items")+" (id integer)")
			spExec(t, sess, "INSERT INTO "+pg.QuoteQualified(schema, "items")+" VALUES (1)")
			t.Cleanup(func() {
				_, _ = sess.Execute(ctx, models.Query{SQL: "ROLLBACK"})
				_, err := sess.Execute(ctx, models.Query{SQL: "DROP SCHEMA " + pg.QuoteIdent(schema) + " CASCADE"})
				if err != nil {
					t.Errorf("owned schema cleanup: %v", err)
				}
			})
			run := func(sql string) error {
				stream, err := sess.Stream(ctx, models.Query{SQL: sql, DefaultSchema: schema})
				if err != nil {
					return err
				}
				defer func() { _ = stream.Close() }()
				for {
					_, ok, err := stream.Next(ctx)
					if err != nil || !ok {
						return err
					}
				}
			}
			for _, sql := range []string{"BEGIN", "SAVEPOINT retained", "INSERT INTO items VALUES (2)"} {
				if err := run(sql); err != nil {
					t.Fatal(err)
				}
			}
			if err := run("SELECT 1/0"); err == nil {
				t.Fatal("fixture must abort the transaction")
			}
			if status, _ := sess.LiveTxStatus(); status != models.TxAbortedInTx {
				t.Fatalf("failed statement left transaction status %q", status)
			}
			if err := run(recovery); err != nil {
				t.Fatalf("schema setup blocked transaction recovery: %v", err)
			}
			wantStatus := models.TxStatus("")
			if recovery == "ROLLBACK TO SAVEPOINT retained" {
				wantStatus = models.TxActive
			}
			if status, _ := sess.LiveTxStatus(); status != wantStatus {
				t.Fatalf("recovery status=%q, want %q", status, wantStatus)
			}
			if err := run("SELECT id FROM items"); err != nil {
				t.Fatalf("selected schema lost after recovery: %v", err)
			}
			if recovery == "ROLLBACK TO SAVEPOINT retained" {
				if err := run("ROLLBACK"); err != nil {
					t.Fatal(err)
				}
			}
			result, err := sess.Execute(ctx, models.Query{SQL: "SELECT count(*) FROM " + pg.QuoteQualified(schema, "items")})
			if err != nil || len(result.Rows) != 1 || result.Rows[0].Values[0] != int64(1) {
				t.Fatal("aborted transaction recovery persisted uncommitted work")
			}
		})
	}
}
