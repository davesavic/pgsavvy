package query

import "testing"

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
