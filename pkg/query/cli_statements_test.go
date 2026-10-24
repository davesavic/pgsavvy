package query

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMachineCLIBatchAnalysis(t *testing.T) {
	tests := []struct {
		name  string
		sql   string
		count int
	}{
		{"plain", "SELECT 1", 1},
		{"empty segments", ";; /* ignored */ ; SELECT 1;;-- tail", 1},
		{"quotes", "SELECT 'a;''b', \"a;\"\"b\"; DELETE FROM t", 2},
		{"standard strings", `SELECT '\'; DELETE FROM t`, 2},
		{"escape string", `SELECT E'a\';b', e'\\'; DELETE FROM t`, 2},
		{"escape continuation", "SELECT E'first'\n'\\';still literal'; DELETE FROM t", 2},
		{"escape CR continuation", "SELECT E'first'-- comment\r'\\';still literal'; DELETE FROM t", 2},
		{"block prevents continuation", "SELECT E'first'\n/* gap */'\\'; DELETE FROM t", 2},
		{"bit hex national Unicode", `SELECT B'1;0', X'F;F', N'a;b', U&'a;\0062', U&"a;\0062"; SELECT 2`, 2},
		{"dollar tags", "DO $$BEGIN PERFORM ';'; END$$; SELECT $a_9$;$other$;$a_9$", 2},
		{"Unicode dollar tag", "SELECT $é_9$a;b$é_9$; SELECT 2", 2},
		{"dollar identifier", "SELECT name$tag$ FROM t; DELETE FROM t", 2},
		{"Unicode identifier", "SELECT café$tag$, 日本語 FROM t; SELECT 2", 2},
		{"nested comments", "/* a /* ; */ b */ SELECT 1; /* ; */ SELECT 2", 2},
		{"line LF", "SELECT 1 -- ; DELETE\n; SELECT 2", 2},
		{"line CR", "SELECT 1 -- ; DELETE\r; SELECT 2", 2},
		{"line CRLF", "SELECT 1 -- ; DELETE\r\n; SELECT 2", 2},
		{"atomic", "CREATE FUNCTION f() RETURNS int LANGUAGE SQL BEGIN ATOMIC SELECT 1; SELECT 2; END; DELETE FROM t", 2},
		{"atomic case", "CREATE OR REPLACE FUNCTION f() RETURNS int LANGUAGE SQL BEGIN ATOMIC SELECT CASE WHEN true THEN CASE WHEN false THEN 1 ELSE 2 END ELSE 3 END; SELECT (CASE WHEN true THEN 1 END); END; SELECT 3", 2},
		{"atomic procedure", "CREATE PROCEDURE p() LANGUAGE SQL BEGIN /*a*/ ATOMIC SELECT 'END;'; END; SELECT 2", 2},
		{"nested routine", "CREATE PROCEDURE p() LANGUAGE SQL BEGIN ATOMIC CREATE FUNCTION f() RETURNS int LANGUAGE SQL BEGIN ATOMIC SELECT 1; END; SELECT 2; END; SELECT 3", 2},
		{"transaction not atomic", "BEGIN; SELECT 1; END;", 3},
		{"begin column", "CREATE FUNCTION f() RETURNS int LANGUAGE SQL BEGIN ATOMIC SELECT begin FROM t; END; SELECT 2", 2},
		{"maintenance", "VACUUM; ANALYZE; REINDEX TABLE t; REFRESH MATERIALIZED VIEW v; SET work_mem='1MB'", 5},
		{"rule actions", "CREATE RULE r AS ON INSERT TO t DO (INSERT INTO u VALUES (1); NOTIFY changed;); SELECT 1", 2},
		{"server copy", "COPY t FROM '/server/file'; COPY (SELECT 1) TO '/server/file'; COPY t TO PROGRAM 'cat >/server/file'", 3},
		{"copy words", "SELECT 'COPY t FROM STDIN', $$COPY t TO STDOUT$$, \"copy\" FROM t /* COPY t FROM STDIN */", 1},
		{"copy option words", "COPY (SELECT 'FROM STDIN') TO '/server/file' WITH (NULL 'TO STDOUT')", 1},
		{"copy named identifiers", `COPY "from"."stdin" ("to", "stdout") FROM '/server/file'`, 1},
		{"Unicode not keyword folded", "SELECT ſtdin, caſe, endé FROM t; SELECT 2", 2},
		{"escape Unicode and bytes", `SELECT E'\uD83D\uDE00', E'\U0001F600', E'\xc3\xa9', E'\xZ', E'\101\303\251'`, 1},
		{"numeric forms", "SELECT 0x_1f, 0o_17, 0b_10, 1_000, .1_5e-2, 1.e+2, 1..2; SELECT 2", 2},
		{"deep comments", strings.Repeat("/*", 256) + ";" + strings.Repeat("*/", 256) + "SELECT 1; SELECT 2", 2},
		{"operator comments", "SELECT 1+/* ; */2; SELECT 3+-- ;\r4", 2},
		{"ordinary then E continuation", "SELECT 'a'\nE'\\';'; SELECT 2", 2},
		{"continued byte UTF8", "SELECT E'\\xc3'\n'\\xa9'; SELECT 2", 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := AnalyzeCLIStatements(context.Background(), tc.sql, 0, true, true)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Statements) != tc.count {
				t.Fatalf("count = %d, want %d", len(got.Statements), tc.count)
			}
			last := -1
			var writes, ddl bool
			for _, statement := range got.Statements {
				if statement.Start <= last || statement.End <= statement.Start || statement.End > len(tc.sql) {
					t.Fatalf("invalid range: %+v", statement)
				}
				if statement.End < len(tc.sql) && tc.sql[statement.End] != ';' {
					t.Fatalf("range does not end at separator: %+v", statement)
				}
				kind := EffectiveKind(tc.sql[statement.Start:statement.End])
				if statement.Kind != kind {
					t.Fatalf("kind = %v, want unchanged EffectiveKind %v", statement.Kind, kind)
				}
				writes = writes || kind == KindDML
				ddl = ddl || kind == KindDDL
				last = statement.End
			}
			if got.WritesApprovalRequired != writes || got.DDLApprovalRequired != ddl {
				t.Fatalf("approval mismatch: %+v", got)
			}
		})
	}

	t.Run("exact original ranges", func(t *testing.T) {
		sql := ";-- empty\r; /* lead */ SELECT 'é;';\nDELETE FROM t; -- tail"
		got, err := AnalyzeCLIStatements(context.Background(), sql, 0, true, true)
		if err != nil {
			t.Fatal(err)
		}
		want := []CLIStatement{{Kind: KindOther}, {Kind: KindDML}}
		// Compute byte positions rather than treating Unicode characters as bytes.
		want[0].Start = strings.Index(sql, " /* lead */")
		want[0].End = strings.Index(sql, ";\nDELETE")
		want[1].Start = want[0].End + 1
		want[1].End = strings.Index(sql, "; -- tail")
		if !reflect.DeepEqual(got.Statements, want) {
			t.Fatalf("ranges = %+v, want %+v", got.Statements, want)
		}
	})

	t.Run("approval truth table and heuristic parity", func(t *testing.T) {
		for _, sql := range []string{
			"DELETE FROM t; CREATE TABLE t(a int)",
			"WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d",
			"SELECT 'DELETE'", "SELECT side_effect()", "CALL opaque()", "DO $$BEGIN EXECUTE 'DROP TABLE t'; END$$",
			"-- CR heuristic remains unchanged\rCREATE TABLE t(a int)",
		} {
			for _, writes := range []bool{false, true} {
				for _, ddl := range []bool{false, true} {
					got, err := AnalyzeCLIStatements(context.Background(), sql, 0, writes, ddl)
					if err != nil {
						t.Fatal(err)
					}
					var wantWrites, wantDDL bool
					for _, st := range got.Statements {
						kind := EffectiveKind(sql[st.Start:st.End])
						wantWrites = wantWrites || writes && kind == KindDML
						wantDDL = wantDDL || ddl && kind == KindDDL
					}
					if got.WritesApprovalRequired != wantWrites || got.DDLApprovalRequired != wantDDL {
						t.Fatalf("flags %v/%v: %+v", writes, ddl, got)
					}
				}
			}
		}
	})

	t.Run("parameterized trailing separators", func(t *testing.T) {
		got, err := AnalyzeCLIStatements(context.Background(), "SELECT $1;; /* tail */; -- end", 1, true, true)
		if err != nil || len(got.Statements) != 1 {
			t.Fatalf("%+v, %v", got, err)
		}
	})

	bad := []struct {
		name, sql string
		params    int
		want      error
	}{
		{"empty", "", 0, ErrInvalidCLIInput},
		{"whitespace", " \r\n\t\v\f", 0, ErrInvalidCLIInput},
		{"comments", ";-- end\r;/* outer /* nested */ end */;", 0, ErrInvalidCLIInput},
		{"negative params", "SELECT 1", -1, ErrInvalidCLIInput},
		{"UTF8", "SELECT '\xff'", 0, ErrInvalidCLIInput},
		{"NUL", "SELECT '\x00'", 0, ErrInvalidCLIInput},
		{"single quote", "DELETE FROM t; SELECT 'secret", 0, ErrUnsupportedCLISQL},
		{"double quote", `SELECT "secret`, 0, ErrUnsupportedCLISQL},
		{"empty identifier", `SELECT ""`, 0, ErrUnsupportedCLISQL},
		{"block comment", "SELECT 1; /* /* */", 0, ErrUnsupportedCLISQL},
		{"dollar quote", "SELECT $tag$secret$other$", 0, ErrUnsupportedCLISQL},
		{"dollar case sensitive", "SELECT $Tag$secret$tag$", 0, ErrUnsupportedCLISQL},
		{"escape ending", `SELECT E'secret\'`, 0, ErrUnsupportedCLISQL},
		{"atomic ending", "CREATE PROCEDURE p() LANGUAGE SQL BEGIN ATOMIC SELECT 1;", 0, ErrUnsupportedCLISQL},
		{"parameter batch", "SELECT $1; DELETE FROM t", 1, ErrUnsupportedCLISQL},
		{"copy stdin", "COPY t FROM STDIN", 0, ErrUnsupportedCLISQL},
		{"copy stdout", "COPY (SELECT 1) TO STDOUT", 0, ErrUnsupportedCLISQL},
		{"copy reverse stdout", "COPY t FROM STDOUT", 0, ErrUnsupportedCLISQL},
		{"copy reverse stdin", "COPY t TO STDIN", 0, ErrUnsupportedCLISQL},
		{"copy mixed", "DELETE FROM t; COPY t /* gap */ FROM -- gap\r STDIN", 0, ErrUnsupportedCLISQL},
		{"copy nested body", "CREATE PROCEDURE p() LANGUAGE SQL BEGIN ATOMIC COPY t FROM STDIN; END", 0, ErrUnsupportedCLISQL},
		{"copy query nesting", "COPY (SELECT * FROM (SELECT 1) s) TO /* gap */ STDOUT", 0, ErrUnsupportedCLISQL},
		{"escape short unicode", `SELECT E'\u12'`, 0, ErrUnsupportedCLISQL},
		{"escape surrogate", `SELECT E'\uD800x'`, 0, ErrUnsupportedCLISQL},
		{"escape NUL", `SELECT E'\000'`, 0, ErrUnsupportedCLISQL},
		{"escape invalid utf8", `SELECT E'\xff'`, 0, ErrUnsupportedCLISQL},
		{"numeric junk", "SELECT 123abc; DELETE FROM t", 0, ErrUnsupportedCLISQL},
		{"numeric exponent", "SELECT 1.2e+", 0, ErrUnsupportedCLISQL},
		{"hex missing digits", "SELECT 0x_", 0, ErrUnsupportedCLISQL},
		{"parameter junk", "SELECT $1foo", 1, ErrUnsupportedCLISQL},
		{"operator too long", "SELECT 1 " + strings.Repeat("@", 64) + " 2", 0, ErrUnsupportedCLISQL},
		{"close parenthesis", "SELECT 1)", 0, ErrUnsupportedCLISQL},
		{"open parenthesis", "SELECT (1", 0, ErrUnsupportedCLISQL},
		{"escape low surrogate", `SELECT E'\uDC00'`, 0, ErrUnsupportedCLISQL},
		{"escape above Unicode", `SELECT E'\U00110000'`, 0, ErrUnsupportedCLISQL},
		{"escape hex NUL", `SELECT E'\x00'`, 0, ErrUnsupportedCLISQL},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			got, err := AnalyzeCLIStatements(context.Background(), tc.sql, tc.params, true, true)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if !reflect.DeepEqual(got, CLIStatementAnalysis{}) {
				t.Fatalf("nonzero result on error: %+v", got)
			}
			if err.Error() != tc.want.Error() {
				t.Fatal("error contains extra input details")
			}
		})
	}

	t.Run("size boundary", func(t *testing.T) {
		for _, size := range []int{1048575, 1048576, 1048577} {
			sql := "SELECT 1" + strings.Repeat(" ", size-len("SELECT 1"))
			got, err := AnalyzeCLIStatements(context.Background(), sql, 0, true, true)
			if size <= 1048576 {
				if err != nil || len(got.Statements) != 1 {
					t.Fatalf("size %d: %+v %v", size, got, err)
				}
			} else if !errors.Is(err, ErrInvalidCLIInput) || !reflect.DeepEqual(got, CLIStatementAnalysis{}) {
				t.Fatalf("size %d: %+v %v", size, got, err)
			}
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		got, err := AnalyzeCLIStatements(ctx, "SELECT 1", 0, true, true)
		if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, CLIStatementAnalysis{}) {
			t.Fatalf("%+v %v", got, err)
		}
	})
	t.Run("disabled approvals do not disable preflight", func(t *testing.T) {
		for _, sql := range []string{"COPY t FROM STDIN", "SELECT 1; SELECT 2"} {
			got, err := AnalyzeCLIStatements(context.Background(), sql, 1, false, false)
			if !errors.Is(err, ErrUnsupportedCLISQL) || !reflect.DeepEqual(got, CLIStatementAnalysis{}) {
				t.Fatalf("%+v %v", got, err)
			}
		}
	})
	t.Run("deadline", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
		defer cancel()
		got, err := AnalyzeCLIStatements(ctx, "SELECT 1", 0, true, true)
		if !errors.Is(err, context.DeadlineExceeded) || !reflect.DeepEqual(got, CLIStatementAnalysis{}) {
			t.Fatalf("%+v %v", got, err)
		}
	})
	t.Run("cancellation after partial analysis", func(t *testing.T) {
		base, cancel := context.WithCancel(context.Background())
		defer cancel()
		ctx := &cliAnalysisCancelContext{Context: base, cancel: cancel, remaining: 10}
		got, err := AnalyzeCLIStatements(ctx, "DELETE FROM t; SELECT 1; CREATE TABLE t(a int)", 0, true, true)
		if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, CLIStatementAnalysis{}) {
			t.Fatalf("%+v %v", got, err)
		}
	})
}

func TestMachineCLIBatchAnalysisGrammarContexts(t *testing.T) {
	for _, alias := range []string{"AS case", "case", "AS end", "end", `AS "case"`, `"end"`} {
		t.Run("atomic alias "+alias, func(t *testing.T) {
			sql := "CREATE FUNCTION pg_temp.f() RETURNS int LANGUAGE SQL BEGIN ATOMIC SELECT 1 " + alias + "; END; DELETE FROM pg_temp.t RETURNING 1 AS end"
			got, err := AnalyzeCLIStatements(context.Background(), sql, 0, true, false)
			if err != nil || len(got.Statements) != 2 || !got.WritesApprovalRequired || got.DDLApprovalRequired {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
	for _, body := range []string{
		"SELECT t.case, t.end FROM (SELECT 1 AS case, 2 AS end) t;",
		"SELECT CASE WHEN true THEN CASE WHEN false THEN 1 ELSE 2 END ELSE 3 END AS case;",
		"SELECT (CASE WHEN true THEN 1 END) end;",
		"SELECT begin atomic FROM (SELECT 1 AS begin) t;",
		"SELECT 1 AS begin, 2 AS atomic;",
		"; SELECT 1 AS end; ; SELECT 2 case;",
	} {
		t.Run("atomic expression "+body, func(t *testing.T) {
			sql := "CREATE FUNCTION pg_temp.f() RETURNS int LANGUAGE SQL BEGIN ATOMIC " + body + " END; DELETE FROM pg_temp.t"
			got, err := AnalyzeCLIStatements(context.Background(), sql, 0, true, false)
			if err != nil || len(got.Statements) != 2 || !got.WritesApprovalRequired {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
	for _, signature := range []string{
		"CREATE FUNCTION pg_temp.f(begin atomic)",
		"CREATE FUNCTION pg_temp.begin(atomic int)",
		"CREATE FUNCTION pg_temp.f(begin int DEFAULT (SELECT 1 atomic))",
	} {
		t.Run("signature "+signature, func(t *testing.T) {
			sql := signature + " RETURNS int LANGUAGE SQL BEGIN ATOMIC SELECT 1; END; DELETE FROM pg_temp.t"
			got, err := AnalyzeCLIStatements(context.Background(), sql, 0, true, false)
			if err != nil || len(got.Statements) != 2 || !got.WritesApprovalRequired {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
	for _, prefix := range []string{
		"COPY pg_temp.from", "COPY pg_temp.to", "COPY db.pg_temp.from", "COPY pg_temp.from (a,b)",
		"COPY BINARY pg_temp.to", `COPY "pg_temp"."from" ("to")`, "COPY (SELECT 1 AS from)",
		`COPY U&"pg_temp" UESCAPE '!'.U&"!0066rom" UESCAPE '!'`,
	} {
		directions := []string{"FROM STDIN", "TO STDOUT", "FROM STDOUT", "TO STDIN"}
		destinations := []string{"FROM '/server/file'", "TO PROGRAM 'cat >/server/file'"}
		if strings.HasPrefix(prefix, "COPY (") {
			directions = []string{"TO STDOUT", "TO STDIN"}
			destinations = []string{"TO '/server/file'", "TO PROGRAM 'cat >/server/file'"}
		}
		for _, direction := range directions {
			t.Run(prefix+" "+direction, func(t *testing.T) {
				got, err := AnalyzeCLIStatements(context.Background(), "DELETE FROM t; "+prefix+" "+direction, 0, true, false)
				if !errors.Is(err, ErrUnsupportedCLISQL) || !reflect.DeepEqual(got, CLIStatementAnalysis{}) {
					t.Fatalf("%+v %v", got, err)
				}
			})
		}
		for _, destination := range destinations {
			t.Run(prefix+" "+destination, func(t *testing.T) {
				got, err := AnalyzeCLIStatements(context.Background(), prefix+" "+destination, 0, true, true)
				if err != nil || len(got.Statements) != 1 {
					t.Fatalf("%+v %v", got, err)
				}
			})
		}
	}
	for _, sql := range []string{
		"COPY pg_temp.from TO '/server/file' USING DELIMITERS ',' WITH NULL AS ''",
		"COPY BINARY pg_temp.to FROM '/server/file'",
		"COPY pg_temp.from (a,b) TO '/server/file' WITH (FORMAT CSV, NULL 'STDOUT')",
	} {
		t.Run("legacy server "+sql, func(t *testing.T) {
			got, err := AnalyzeCLIStatements(context.Background(), sql, 0, true, true)
			if err != nil || len(got.Statements) != 1 {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
	for _, query := range []string{
		"INSERT INTO t VALUES (1) RETURNING *",
		"WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d",
	} {
		t.Run("streaming modifying COPY "+query, func(t *testing.T) {
			got, err := AnalyzeCLIStatements(context.Background(), "SELECT 1; COPY ("+query+") TO STDOUT", 0, true, true)
			if !errors.Is(err, ErrUnsupportedCLISQL) || !reflect.DeepEqual(got, CLIStatementAnalysis{}) {
				t.Fatalf("%+v %v", got, err)
			}
		})
		t.Run("server modifying COPY "+query, func(t *testing.T) {
			got, err := AnalyzeCLIStatements(context.Background(), "COPY ("+query+") TO '/server/file'", 0, true, true)
			if err != nil || len(got.Statements) != 1 || !got.WritesApprovalRequired {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
	t.Run("streaming COPY options WHERE", func(t *testing.T) {
		got, err := AnalyzeCLIStatements(context.Background(), "COPY t FROM STDIN WITH (FORMAT CSV) WHERE id > 0", 0, false, false)
		if !errors.Is(err, ErrUnsupportedCLISQL) || !reflect.DeepEqual(got, CLIStatementAnalysis{}) {
			t.Fatalf("%+v %v", got, err)
		}
	})
	t.Run("server COPY options WHERE", func(t *testing.T) {
		got, err := AnalyzeCLIStatements(context.Background(), "COPY t FROM '/server/file' WITH (FORMAT CSV) WHERE id > 0", 0, true, true)
		if err != nil || len(got.Statements) != 1 {
			t.Fatalf("%+v %v", got, err)
		}
	})
}

func TestMachineCLIBatchAnalysisOperatorWork(t *testing.T) {
	for _, prefix := range []string{"", "=", "<>"} {
		t.Run("cached signs "+prefix, func(t *testing.T) {
			sql := prefix + strings.Repeat("+-+", 4096) + "1"
			lexer := cliLexer{ctx: context.Background(), sql: sql}
			token, err := lexer.next()
			firstEnd := len(prefix)
			if firstEnd == 0 {
				firstEnd = 1
			}
			if err != nil || token.start != 0 || token.end != firstEnd {
				t.Fatalf("%+v %v", token, err)
			}
			// The trimmed suffix must remain cached, not be rescanned per token.
			if lexer.operatorSuffixEnd != len(sql)-1 {
				t.Fatalf("uncached suffix end = %d, want %d", lexer.operatorSuffixEnd, len(sql)-1)
			}
			for i := firstEnd; i < len(sql)-1; i++ {
				token, err = lexer.next()
				if err != nil || token.start != i || token.end != i+1 || lexer.operatorSuffixEnd != len(sql)-1 {
					t.Fatalf("at %d: %+v %v cache=%d", i, token, err, lexer.operatorSuffixEnd)
				}
			}
		})
	}
	for _, input := range []struct {
		sql  string
		ends []int
	}{
		{"++--comment\r1", []int{1, 2, 13}},
		{"=+-/*comment*/1", []int{1, 2, 3, 15}},
		{"@+++1", []int{4, 5}},
		{"++?--comment\n1", []int{3, 14}},
	} {
		t.Run("token boundaries "+input.sql, func(t *testing.T) {
			lexer := cliLexer{ctx: context.Background(), sql: input.sql}
			for _, end := range input.ends {
				token, err := lexer.next()
				if err != nil || token.end != end {
					t.Fatalf("%+v %v, want end %d", token, err, end)
				}
			}
		})
	}
	t.Run("maximum valid unary run", func(t *testing.T) {
		sql := "SELECT " + strings.Repeat("+", 1048576-len("SELECT 1")) + "1"
		got, err := AnalyzeCLIStatements(context.Background(), sql, 0, true, true)
		if err != nil || len(got.Statements) != 1 || got.Statements[0].End != len(sql) {
			t.Fatalf("%+v %v", got, err)
		}
	})
	for _, phase := range []struct {
		name, sql string
		remaining int
	}{
		{"forward", strings.Repeat("@", 16384) + "1", 2},
		{"trim", strings.Repeat("+", 16384) + "1", 5},
	} {
		t.Run("cancel "+phase.name, func(t *testing.T) {
			base, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &cliAnalysisCancelContext{Context: base, cancel: cancel, remaining: phase.remaining}
			lexer := cliLexer{ctx: ctx, sql: phase.sql}
			_, err := lexer.next()
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context cancellation inside %s loop", err, phase.name)
			}
		})
	}
}

func BenchmarkMachineCLIBatchAnalysisOperators(b *testing.B) {
	for _, size := range []int{4096, 8192, 16384, 32768} {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			sql := "SELECT " + strings.Repeat("+", size) + "1"
			b.SetBytes(int64(len(sql)))
			for b.Loop() {
				if _, err := AnalyzeCLIStatements(context.Background(), sql, 0, true, true); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

type cliAnalysisCancelContext struct {
	context.Context
	cancel    context.CancelFunc
	remaining int
}

func (c *cliAnalysisCancelContext) Err() error {
	c.remaining--
	if c.remaining == 0 {
		c.cancel()
	}
	return c.Context.Err()
}
