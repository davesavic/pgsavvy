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
