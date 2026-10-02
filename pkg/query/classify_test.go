package query

import (
	"strings"
	"testing"
)

func TestReview_classify_nested_comments(t *testing.T) {
	tests := []struct {
		name      string
		sql       string
		classify  StatementKind
		effective StatementKind
	}{
		{"nested drop", "/* outer /* inner */ tail */ DROP TABLE t", KindDDL, KindDDL},
		{"nested create", "/* outer /* inner */ tail */ CREATE TABLE t (id int)", KindDDL, KindDDL},
		{"three levels", "/* a /* b /* c */ b */ a */ ALTER TABLE t ADD COLUMN c int", KindDDL, KindDDL},
		{"comment dml before ddl", "/* a /* b */ DELETE FROM ignored */ DROP TABLE t", KindDDL, KindDDL},
		{"comment ddl before select", "/* a /* b */ DROP TABLE ignored */ SELECT 1", KindOther, KindOther},
		{"nested dml", "/* a /* b */ a */ UPDATE t SET a=1", KindDML, KindDML},
		{"nested select", "/* a /* b */ a */ SELECT 1", KindOther, KindOther},
		{"nested writable cte", "/* a /* b */ a */ WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d", KindOther, KindDML},
		{"adjacent comments", "/* a /* b */ a *//* next */DROP TABLE t", KindDDL, KindDDL},
		{"mixed comments and whitespace", " \t-- first\n/* a /* b */ a */\n-- next\n\t/* last */ drop table t", KindDDL, KindDDL},
		{"empty nested comment", "/**//*/**/*/DROP TABLE t", KindDDL, KindDDL},
		{"quoted markers still nest", "/* '/*' nested '*/' */ DROP TABLE t", KindDDL, KindDDL},
		{"line marker inside block", "/* -- /* inner */ tail */ DROP TABLE t", KindDDL, KindDDL},
		{"comment only", "/* a /* b */ a */", KindOther, KindOther},
		{"comment then whitespace", "/* a /* b */ a */ \n\t", KindOther, KindOther},
		{"comment then final line comment", "/* a /* b */ a */-- end", KindOther, KindOther},
		{"unterminated outer", "/* a /* b */ DROP TABLE t", KindOther, KindOther},
		{"unterminated inner", "/* a /* b DROP TABLE t", KindOther, KindOther},
		{"bare opening marker", "/*", KindOther, KindOther},
		{"trailing partial delimiter", "/* a /* b */ *", KindOther, KindOther},
		{"nonoverlapping delimiters", "/*/DROP TABLE t", KindOther, KindOther},
		{"select ddl named columns", "/* a /* b */ a */ SELECT comment, drop FROM t", KindOther, KindOther},
		{"deep nesting", strings.Repeat("/*", 256) + "note" + strings.Repeat("*/", 256) + "DROP TABLE t", KindDDL, KindDDL},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.sql); got != tc.classify {
				t.Errorf("Classify = %v, want %v", got, tc.classify)
			}
			if got := EffectiveKind(tc.sql); got != tc.effective {
				t.Errorf("EffectiveKind = %v, want %v", got, tc.effective)
			}
		})
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want StatementKind
	}{
		{"select", "SELECT * FROM t", KindOther},
		{"lowercase select", "select 1", KindOther},
		{"with cte select", "WITH x AS (SELECT 1) SELECT * FROM x", KindOther},
		{"show", "SHOW search_path", KindOther},
		{"explain", "EXPLAIN SELECT 1", KindOther},
		{"empty", "", KindOther},
		{"only comment", "-- just a comment", KindOther},

		{"update", "UPDATE users SET name='x' WHERE id=1", KindDML},
		{"insert", "INSERT INTO t VALUES (1)", KindDML},
		{"delete", "DELETE FROM t WHERE id=1", KindDML},
		{"merge", "MERGE INTO t USING s ON t.id=s.id", KindDML},
		{"lowercase update", "update t set a=1", KindDML},
		{"leading whitespace", "   \n\t UPDATE t SET a=1", KindDML},
		{"leading line comment", "-- bump\nUPDATE t SET a=1", KindDML},
		{"leading block comment", "/* note */ DELETE FROM t", KindDML},
		{"multi line comments", "--a\n--b\n  insert into t values(1)", KindDML},

		{"create", "CREATE TABLE t (id int)", KindDDL},
		{"alter", "ALTER TABLE t ADD COLUMN c int", KindDDL},
		{"drop", "DROP TABLE t", KindDDL},
		{"truncate", "TRUNCATE t", KindDDL},
		{"comment on", "COMMENT ON TABLE t IS 'x'", KindDDL},
		{"grant", "GRANT SELECT ON t TO u", KindDDL},
		{"revoke", "REVOKE SELECT ON t FROM u", KindDDL},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.sql); got != tc.want {
				t.Errorf("Classify(%q) = %v, want %v", tc.sql, got, tc.want)
			}
		})
	}
}

func TestEffectiveKind(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want StatementKind
	}{
		{"plain select stays other", "SELECT * FROM t", KindOther},
		{"plain delete stays dml", "DELETE FROM t WHERE id=1", KindDML},
		{"plain create stays ddl", "CREATE TABLE t (id int)", KindDDL},
		{"writable cte delete elevates", "WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d", KindDML},
		{"writable cte insert elevates", "WITH n AS (INSERT INTO logs VALUES (1) RETURNING id) SELECT * FROM n", KindDML},
		{"writable cte update elevates", "WITH u AS (UPDATE t SET a=1 RETURNING *) SELECT * FROM u", KindDML},
		{"read-only cte does not elevate", "WITH x AS (SELECT 1) SELECT * FROM x", KindOther},
		{"column named updated_at no elevation", "SELECT id, updated_at FROM events", KindOther},
		// Known limitation: a DML keyword inside a string literal falsely
		// elevates (fail-closed bias). Documents actual behavior.
		{"delete inside string literal falsely elevates", "SELECT 'DELETE' AS note", KindDML},
		// DDL-CTE verdict: NO ddlTokenRE elevation.
		// A WITH-led statement cannot execute DDL in Postgres, and a benign
		// read-only SELECT with a DDL-keyword-named column (e.g. "comment") must
		// NOT be elevated to KindDDL (else the pre-run ConfirmDDL prompt spuri-
		// ously fires). These pin that EffectiveKind leaves them KindOther.
		{"refresh matview stays other (leads with REFRESH, not in keyword table)", "REFRESH MATERIALIZED VIEW mv", KindOther},
		{"column named comment stays other", "SELECT id, comment FROM posts", KindOther},
		{"column named drop stays other", "SELECT drop FROM measurements", KindOther},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveKind(tc.sql); got != tc.want {
				t.Errorf("EffectiveKind(%q) = %v, want %v", tc.sql, got, tc.want)
			}
		})
	}
}
