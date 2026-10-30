//go:build linux || darwin

package pg

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/cockroachdb-parser/pkg/sql/parser"
	"github.com/cockroachdb/cockroachdb-parser/pkg/sql/sem/tree"

	"github.com/davesavic/pgsavvy/pkg/query"
)

func TestTemporaryScriptAccepted(t *testing.T) {
	for _, sql := range []string{
		"SELECT 1", "VALUES (1),(2)", "TABLE exact_name",
		"SELECT NULL, 'null', 9223372036854775807::int8, 123.456::numeric, ARRAY[1,2]",
		"WITH a AS (SELECT 1) SELECT * FROM a",
		"WITH RECURSIVE a(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM a WHERE n<3) SELECT * FROM a",
		"SELECT CASE WHEN true THEN (SELECT 1) ELSE 2 END",
		"SELECT * FROM a JOIN (SELECT * FROM b) c ON a.id=c.id",
		"SELECT 1 UNION SELECT 2 INTERSECT SELECT 3 EXCEPT SELECT 4",
		"SELECT count(*) FILTER(WHERE true) OVER (PARTITION BY x ORDER BY y ROWS BETWEEN 1 PRECEDING AND CURRENT ROW) FROM t",
		"SELECT * FROM generate_series(1,3) WITH ORDINALITY AS g(n,i)",
		"SELECT 'é; --\r', \"日本語\" FROM café",
		"SELECT $$; --\r INTO ignored$$, $タグ$'; /* not a comment */$タグ$",
		"SELECT 'it''s; fine', 'plain\\path', E'it\\'s; fine', E'\\n\\t\\x41\\101\\u00e9'",
		"SELECT 1 -- CRLF comment\r\n; SELECT 2",
		"/* outer /* inner ; */ still comment */ SELECT 1; -- suffix",
		"SELECT 1\r; SELECT 2",
	} {
		t.Run(sql, func(t *testing.T) {
			script, err := ValidateTemporaryScript(context.Background(), sql, 0, false)
			if err != nil || !script.validated || script.HasWrites() || script.SQL() != sql {
				t.Fatalf("read corpus: valid=%v writes=%v sql preserved=%v err=%v", script.validated, script.HasWrites(), script.SQL() == sql, err)
			}
		})
	}
	for _, sql := range []string{
		"INSERT INTO t VALUES(1)", "INSERT INTO t DEFAULT VALUES",
		"INSERT INTO t(x) SELECT x FROM s RETURNING x",
		"INSERT INTO t VALUES(1) ON CONFLICT DO NOTHING",
		"INSERT INTO t VALUES(1) ON CONFLICT(x) WHERE x>0 DO UPDATE SET x=excluded.x WHERE t.x>0 RETURNING *",
		"INSERT INTO t VALUES(1) ON CONFLICT ON CONSTRAINT t_pkey DO UPDATE SET x=excluded.x",
		"UPDATE t SET x=1 WHERE x=2 RETURNING *",
		"UPDATE t SET (x,y)=(SELECT x,y FROM s) FROM s WHERE t.id=s.id RETURNING t.*",
		"DELETE FROM t", "DELETE FROM t WHERE x=1 RETURNING *",
		"WITH a AS (DELETE FROM t RETURNING *) SELECT * FROM a",
		"WITH a AS (INSERT INTO t VALUES(1) RETURNING *) UPDATE t SET x=2 RETURNING *",
		"SELECT 1; INSERT INTO t VALUES(1); UPDATE t SET x=2; DELETE FROM t RETURNING *; SELECT 2",
	} {
		t.Run(sql, func(t *testing.T) {
			script, err := ValidateTemporaryScript(context.Background(), sql, 0, true)
			if err != nil || !script.validated || !script.HasWrites() || !script.AllowsWrites() || script.SQL() != sql {
				t.Fatalf("write corpus: valid=%v writes=%v err=%v", script.validated, script.HasWrites(), err)
			}
			assertTemporaryRejection(t, context.Background(), sql, 0, false, "POLICY_DENIED")
		})
	}
}

func TestTemporaryScriptOriginalRangesAndParameters(t *testing.T) {
	sql := " ;/* lead */ SELECT ';', $$;$$  ;\n-- write\nINSERT INTO t VALUES(1) RETURNING * ; /*gap*/ ;\tSELECT '末'; -- tail"
	script, err := ValidateTemporaryScript(context.Background(), sql, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/* lead */ SELECT ';', $$;$$  ", "\n-- write\nINSERT INTO t VALUES(1) RETURNING * ", "\tSELECT '末'"}
	parts := script.Statements()
	if len(parts) != len(want) || script.SupportsParameters() || script.ParameterCount() != 0 || script.SQL() != sql {
		t.Fatal("script metadata/original bytes lost")
	}
	last := -1
	for i, part := range parts {
		if part.Start <= last || part.End < part.Start || sql[part.Start:part.End] != want[i] || part.Writes != (i == 1) {
			t.Fatalf("range %d: %+v", i, part)
		}
		last = part.End
	}
	parts[0].Start, parts[1].Writes = 12345, false
	if reflect.DeepEqual(parts, script.Statements()) || !script.Statements()[1].Writes {
		t.Fatal("caller mutated validated metadata")
	}
	parameterized := " /* preserve */ SELECT $2::int, $1::text, $2::int; -- suffix"
	bound, err := ValidateTemporaryScript(context.Background(), parameterized, 2, false)
	if err != nil || bound.SQL() != parameterized || bound.ParameterCount() != 2 || !bound.SupportsParameters() {
		t.Fatalf("parameter handoff: %v", err)
	}
	assertTemporaryRejection(t, context.Background(), "SELECT $1; SELECT 2", 1, false, "UNSUPPORTED_SQL")
	// As with native execution, binding/type compatibility is resolved by the
	// server; this API records the supplied count without rewriting placeholders.
	if _, err := ValidateTemporaryScript(context.Background(), "SELECT $3", 1, false); err != nil {
		t.Fatal(err)
	}
}

func TestTemporaryScriptUnsupportedEverywhere(t *testing.T) {
	for _, sql := range []string{
		"BEGIN", "START TRANSACTION", "END", "COMMIT AND CHAIN", "ABORT", "ROLLBACK", "ROLLBACK TO SAVEPOINT x",
		"SAVEPOINT x", "RELEASE SAVEPOINT x", "PREPARE TRANSACTION 'x'", "COMMIT PREPARED 'x'", "ROLLBACK PREPARED 'x'",
		"SET TRANSACTION READ WRITE", "SET ROLE admin", "RESET ALL", "DISCARD ALL",
		"CREATE TABLE t(x int)", "DROP TABLE t", "ALTER TABLE t ADD y int", "TRUNCATE t",
		"DO $$BEGIN END$$", "CALL f()", "COPY t TO STDOUT", "COPY t FROM STDIN", "COPY t TO '/tmp/not-created'",
		"EXPLAIN SELECT 1", "EXPLAIN ANALYZE DELETE FROM t", "LOCK TABLE t", "SHOW ALL", "PREPARE x AS SELECT 1", "EXECUTE x",
		"SELECT 1 INTO t", "SELECT * FROM t FOR UPDATE", "SELECT * FROM t FOR NO KEY UPDATE", "SELECT * FROM t FOR SHARE", "SELECT * FROM t FOR KEY SHARE",
		"UPSERT INTO t VALUES(1)", "INSERT INTO t VALUES(1) RETURNING NOTHING", "UPDATE t SET x=1 RETURNING NOTHING", "DELETE FROM t RETURNING NOTHING",
		"DELETE FROM t USING s WHERE t.x=s.x", "UPDATE t SET x=1 ORDER BY x LIMIT 1", "DELETE FROM t LIMIT 1",
		"SELECT * FROM [SELECT 1]", "SELECT * FROM [DELETE FROM t RETURNING *]", "SELECT * FROM t@some_index",
		"SELECT * FROM t AS OF SYSTEM TIME '-1s'", "SELECT * FROM t INNER HASH JOIN s ON true", "SELECT x FROM t ORDER BY PRIMARY KEY t",
		"SELECT broken syntax ???", "SELECT '", "SELECT $$unterminated", "SELECT 1 /* unterminated",
	} {
		t.Run(sql, func(t *testing.T) {
			for _, wrapped := range []string{sql, sql + "; SELECT 1", "SELECT 1; " + sql + "; SELECT 2", "INSERT INTO t VALUES(1); " + sql} {
				assertTemporaryRejection(t, context.Background(), wrapped, 0, true, "UNSUPPORTED_SQL")
			}
		})
	}
}

func TestTemporaryScriptNestedTraversal(t *testing.T) {
	readPositions := []string{
		"WITH unused AS (%s) SELECT 1",
		"SELECT (WITH unused AS (%s) SELECT 1)",
		"SELECT * FROM (WITH unused AS (%s) SELECT 1) s",
		"SELECT * FROM t JOIN s ON EXISTS(WITH unused AS (%s) SELECT 1)",
		"SELECT count(*) FILTER(WHERE EXISTS(WITH unused AS (%s) SELECT 1)) FROM t",
		"SELECT sum(x) OVER (PARTITION BY (WITH unused AS (%s) SELECT 1)) FROM t",
		"SELECT sum(x) OVER (ORDER BY (WITH unused AS (%s) SELECT 1)) FROM t",
		"SELECT sum(x) OVER (ROWS BETWEEN (WITH unused AS (%s) SELECT 1) PRECEDING AND CURRENT ROW) FROM t",
		"SELECT 1 WHERE EXISTS(WITH unused AS (%s) SELECT 1)",
		"SELECT 1 GROUP BY (WITH unused AS (%s) SELECT 1)",
		"SELECT 1 HAVING EXISTS(WITH unused AS (%s) SELECT 1)",
		"SELECT 1 ORDER BY (WITH unused AS (%s) SELECT 1)",
		"SELECT 1 LIMIT (WITH unused AS (%s) SELECT 1)",
	}
	for _, position := range readPositions {
		sql := strings.Replace(position, "%s", "DELETE FROM t RETURNING *", 1)
		t.Run(sql, func(t *testing.T) {
			if _, err := parser.ParseOne(sql); err != nil {
				t.Fatalf("fixture must reach traversal: %v", err)
			}
			script, err := ValidateTemporaryScript(context.Background(), sql, 0, true)
			if err != nil || !script.HasWrites() || !script.Statements()[0].Writes {
				t.Fatalf("nested write missed: %v", err)
			}
			assertTemporaryRejection(t, context.Background(), sql, 0, false, "POLICY_DENIED")
		})
	}
	// Root writes alone cannot prove traversal: put a forbidden, parser-supported
	// lock in each child, including conflict arbitration/assignments and RETURNING.
	for _, sql := range []string{
		"INSERT INTO t SELECT x FROM s FOR SHARE",
		"INSERT INTO t VALUES(1) ON CONFLICT(x) WHERE EXISTS(SELECT 1 FROM s FOR SHARE) DO NOTHING",
		"INSERT INTO t VALUES(1) ON CONFLICT(x) DO UPDATE SET x=(SELECT x FROM s FOR SHARE)",
		"INSERT INTO t VALUES(1) ON CONFLICT(x) DO UPDATE SET x=1 WHERE EXISTS(SELECT 1 FROM s FOR SHARE)",
		"INSERT INTO t VALUES(1) RETURNING (SELECT x FROM s FOR SHARE)",
		"UPDATE t SET x=(SELECT x FROM s FOR SHARE)",
		"UPDATE t SET x=1 FROM (SELECT x FROM s FOR SHARE) s",
		"UPDATE t SET x=1 RETURNING (SELECT x FROM s FOR SHARE)",
		"DELETE FROM t WHERE EXISTS(SELECT 1 FROM s FOR SHARE)",
		"DELETE FROM t RETURNING (SELECT x FROM s FOR SHARE)",
		"WITH a AS (SELECT * FROM s FOR SHARE) DELETE FROM t",
		"SELECT * FROM t JOIN s ON EXISTS(SELECT 1 FROM s FOR SHARE)",
		"SELECT sum(x) OVER (ORDER BY (SELECT x FROM s FOR SHARE)) FROM t",
		"SELECT * FROM (WITH unused AS (CREATE TABLE t(x int)) SELECT 1) s",
	} {
		t.Run(sql, func(t *testing.T) {
			if _, err := parser.ParseOne(sql); err != nil {
				t.Fatalf("fixture must reach traversal: %v", err)
			}
			assertTemporaryRejection(t, context.Background(), sql, 0, true, "UNSUPPORTED_SQL")
		})
	}
}

func TestTemporaryScriptLexicalBoundary(t *testing.T) {
	for _, sql := range []string{
		"SELECT 1 -- hidden\r INTO t", "SELECT 1 -- hidden\r INTO t\n", "SELECT 1 -- hidden\r; DELETE FROM t",
		"SELECT 1 -- hidden\r", "SELECT E'\\a'", "SELECT E'\\v'", "SELECT E'\\X41'",
		"SELECT b'\\047; DELETE FROM t'", "SELECT B'101'", "SELECT X'41'", "SELECT N'text'", "SELECT U&'d\\0061t'", "SELECT U&\"name\"",
		"SELECT 'a'\n'b'", "SELECT 'a'\r'b'", "SELECT \"a\"\n\"b\"", "SELECT 1\v",
		"SELECT İD FROM t", "SELECT experimental_null", "SELECT testing_null",
		"SELECT E'\\000'", "SELECT E'\\xff'", "SELECT E'\\u0000'", "SELECT E'\\ud800'",
		"SELECT 1;\\! anything", "SELECT $1junk", "SELECT 1_foo",
	} {
		t.Run(sql, func(t *testing.T) {
			assertTemporaryRejection(t, context.Background(), sql, 0, true, "UNSUPPORTED_SQL")
		})
	}
	// These remain native boundary input, not a newly broadened saved safety
	// validator. The temporary-only guard must not change either existing API.
	for _, sql := range []string{"SELECT 1 -- hidden\r INTO t", "SELECT 'a'\n'b'", "SELECT B'101'"} {
		if _, err := query.AnalyzeCLIStatements(context.Background(), sql, 0, false, false); err != nil {
			t.Fatalf("saved/native boundary changed: %v", err)
		}
		if err := ValidateMachineQuery(context.Background(), sql); err != nil {
			t.Fatalf("saved guarded behavior changed: %v", err)
		}
	}
}

func TestTemporaryScriptBoundsAndContext(t *testing.T) {
	for _, sql := range []string{"", " ; /* comment */", strings.Repeat(" ", 1<<20) + "SELECT 1", "SELECT '\x00'", "SELECT '\xff'"} {
		assertTemporaryRejection(t, context.Background(), sql, 0, true, "INVALID_INPUT")
	}
	for _, count := range []int{-1, 257} {
		assertTemporaryRejection(t, context.Background(), "SELECT 1", count, true, "INVALID_INPUT")
	}
	if _, err := ValidateTemporaryScript(context.Background(), "SELECT 1"+strings.Repeat(" ", (1<<20)-8), 256, false); err != nil {
		t.Fatalf("exact size/count limit: %v", err)
	}
	deep := strings.Repeat("SELECT (", 150) + "SELECT 1" + strings.Repeat(")", 150)
	if _, err := parser.ParseOne(deep); err != nil {
		t.Fatal(err)
	}
	assertTemporaryRejection(t, context.Background(), deep, 0, true, "UNSUPPORTED_SQL")
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	assertTemporaryRejection(t, cancelled, "SELECT 1", 0, false, "TIMEOUT")
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	assertTemporaryRejection(t, expired, "SELECT 1", 0, false, "TIMEOUT")
	// Check cancellation during analysis/lexing/parsing/traversal deterministically,
	// without timing sleeps or leaving parser goroutines behind.
	for _, after := range []int{5, 20, 100, 500} {
		ctx := &temporaryCountingContext{remaining: after}
		assertTemporaryRejection(t, ctx, "SELECT '"+strings.Repeat("x", 2000)+"'", 0, false, "TIMEOUT")
	}
	for _, value := range []reflect.Value{
		reflect.ValueOf(&tree.StatementSource{Statement: &tree.Select{}}),
		reflect.ValueOf(&tree.CreateTable{}),
		reflect.ValueOf(&tree.LiteralValuesClause{}),
	} {
		writes := false
		if err := checkTemporaryAST(context.Background(), value, 0, &writes); err == nil {
			t.Fatal("unknown statement/table variant accepted")
		}
	}
	var zero ValidatedTemporaryScript
	if zero.validated || zero.SQL() != "" || zero.Statements() != nil || zero.SupportsParameters() || zero.HasWrites() || zero.AllowsWrites() {
		t.Fatal("zero artifact is executable")
	}
}

func assertTemporaryRejection(t *testing.T, ctx context.Context, sql string, count int, allowWrite bool, code string) {
	t.Helper()
	script, err := ValidateTemporaryScript(ctx, sql, count, allowWrite)
	var failure *MachineError
	if !errors.As(err, &failure) || failure.Code != code {
		t.Fatalf("rejection: want %s, got %v", code, err)
	}
	if !reflect.DeepEqual(script, ValidatedTemporaryScript{}) {
		t.Fatal("rejection returned a partial executable artifact")
	}
}

type temporaryCountingContext struct {
	context.Context
	remaining int
}

func (c *temporaryCountingContext) Err() error {
	c.remaining--
	if c.remaining <= 0 {
		return context.Canceled
	}
	return nil
}
