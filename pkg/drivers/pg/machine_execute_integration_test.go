//go:build integration && (linux || darwin)

package pg

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/query"
)

type machineExecutePG struct {
	profile                      models.Connection
	observer                     *pgx.Conn
	schema, role, denied, prefix string
}

func machineExecutePGError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	var server *pgconn.PgError
	if errors.As(err, &server) {
		t.Fatalf("fixture SQLSTATE %s", server.Code)
	}
	var safe *MachineError
	if errors.As(err, &safe) {
		t.Fatalf("machine fixture code %s SQLSTATE %s", safe.Code, safe.SQLState)
	}
	t.Fatalf("designated fixture operation failed (%T; details withheld)", err)
}

func machineExecutePGFixture(t *testing.T) *machineExecutePG {
	t.Helper()
	dsn := os.Getenv("PGSAVVY_TEST_PG")
	if dsn == "" {
		t.Fatal("designated isolated PGSAVVY_TEST_PG is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	machineExecutePGError(t, err)
	t.Cleanup(func() { machineExecutePGError(t, admin.Close(context.Background())) })
	var prepared int
	machineExecutePGError(t, admin.QueryRow(ctx, "SELECT current_setting('max_prepared_transactions')::int").Scan(&prepared))
	if prepared < 1 {
		t.Fatal("designated fixture must enable prepared transactions")
	}
	prefix := fmt.Sprintf("pgsavvy_mexec_%d_%d", os.Getpid(), time.Now().UnixNano())
	login, role, denied := prefix+"_l", prefix+"_r", prefix+"_d"
	id := func(s string) string { return pgx.Identifier{s}.Sanitize() }
	var observer *pgx.Conn
	granted := false
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if observer != nil {
			_, err := observer.Exec(cleanup, "DROP SCHEMA IF EXISTS "+id(prefix)+" CASCADE")
			machineExecutePGError(t, err)
			machineExecutePGError(t, observer.Close(cleanup))
		}
		if granted {
			_, err := admin.Exec(cleanup, "REVOKE CREATE ON DATABASE "+id(admin.Config().Database)+" FROM "+id(role))
			machineExecutePGError(t, err)
		}
		for _, name := range []string{login, denied, role} {
			_, err := admin.Exec(cleanup, "DROP ROLE IF EXISTS "+id(name))
			machineExecutePGError(t, err)
		}
	})
	for _, sql := range []string{
		"CREATE ROLE " + id(login) + " LOGIN NOINHERIT PASSWORD 'disposable-machine-test'",
		"CREATE ROLE " + id(role) + " NOLOGIN CREATEDB",
		"CREATE ROLE " + id(denied) + " NOLOGIN",
		"GRANT " + id(role) + ", " + id(denied) + " TO " + id(login),
		"GRANT CREATE ON DATABASE " + id(admin.Config().Database) + " TO " + id(role),
	} {
		_, err := admin.Exec(ctx, sql)
		machineExecutePGError(t, err)
	}
	granted = true
	// The superuser only provisions roles/privileges; all execution and state
	// observations below use the restricted login and configured role.
	cfg := admin.Config().Copy()
	cfg.User, cfg.Password = login, "disposable-machine-test"
	observer, err = pgx.ConnectConfig(ctx, cfg)
	machineExecutePGError(t, err)
	_, err = observer.Exec(ctx, "SET ROLE "+id(role))
	machineExecutePGError(t, err)
	// ConnString preserves the original parsed DSN, not the modified login.
	f := &machineExecutePG{profile: models.Connection{Name: prefix, Driver: "postgres", User: login, Password: cfg.Password, Role: role, CLIAccess: models.CLIAccessFull}, observer: observer, schema: id(prefix), role: role, denied: denied, prefix: prefix}
	f.profile.Host, f.profile.Port, f.profile.Database, f.profile.SSLMode = cfg.Host, int(cfg.Port), cfg.Database, "disable"
	f.must(t, MachineQuery{SQL: "CREATE SCHEMA " + f.schema + "; CREATE TABLE " + f.schema + ".effects(v integer); CREATE TABLE " + f.schema + ".deferred(v integer UNIQUE DEFERRABLE INITIALLY DEFERRED)"})
	return f
}

func (f *machineExecutePG) open(t *testing.T, profile models.Connection) *MachineClient {
	t.Helper()
	client, err := OpenMachine(context.Background(), profile, MachineOptions{RequiredAccess: models.CLIAccessFull})
	machineExecutePGError(t, err)
	t.Cleanup(func() { machineExecutePGError(t, client.Close(context.Background())) })
	return client
}

func (f *machineExecutePG) dsnProfile(settings map[string]string) models.Connection {
	profile := f.profile
	u := url.URL{Scheme: "postgres", User: url.UserPassword(profile.User, profile.Password), Path: "/" + profile.Database}
	values := url.Values{"host": {profile.Host}, "port": {strconv.Itoa(profile.Port)}, "sslmode": {"disable"}}
	for name, value := range settings {
		values.Set(name, value)
	}
	u.RawQuery = values.Encode()
	profile.DSN = u.String()
	return profile
}

func (f *machineExecutePG) execute(t *testing.T, request MachineQuery) (MachineExecuteResult, error) {
	t.Helper()
	client := f.open(t, f.profile)
	result, err := client.Execute(context.Background(), request)
	machineExecutePGError(t, client.Close(context.Background()))
	return result, err
}

func (f *machineExecutePG) must(t *testing.T, request MachineQuery) MachineExecuteResult {
	t.Helper()
	result, err := f.execute(t, request)
	machineExecutePGError(t, err)
	if result.Execution.Status != MachineExecutionCompleted || !result.Execution.EffectsMayHaveOccurred {
		t.Fatalf("completion metadata %+v", result.Execution)
	}
	return result
}

func (f *machineExecutePG) count(t *testing.T, value int) int {
	t.Helper()
	var n int
	machineExecutePGError(t, f.observer.QueryRow(context.Background(), "SELECT count(*) FROM "+f.schema+".effects WHERE v=$1", value).Scan(&n))
	return n
}

func machineExecuteWantError(t *testing.T, result MachineExecuteResult, err error, code, state string, status MachineExecutionStatus) {
	t.Helper()
	var safe *MachineError
	if !errors.As(err, &safe) || safe.Code != code || state != "" && safe.SQLState != state || result.Execution.Status != status || len(result.Results) != 0 {
		t.Fatalf("outcome %+v error %v; want %s/%s/%s", result.Execution, err, code, state, status)
	}
}

func TestMachineExecutePostgresTransactions(t *testing.T) {
	f := machineExecutePGFixture(t)
	table := f.schema + ".effects"
	t.Run("commits and native implicit rollback", func(t *testing.T) {
		f.must(t, MachineQuery{SQL: "INSERT INTO " + table + " VALUES(1); INSERT INTO " + table + " VALUES(1)"})
		if f.count(t, 1) != 2 {
			t.Fatal("implicit batch not committed")
		}
		result, err := f.execute(t, MachineQuery{SQL: "INSERT INTO " + table + " VALUES(2); SELECT 1/0"})
		machineExecuteWantError(t, result, err, "DATABASE_FAILURE", "22012", MachineExecutionFailed)
		if f.count(t, 2) != 0 {
			t.Fatal("implicit batch not atomic")
		}
		result, err = f.execute(t, MachineQuery{SQL: "BEGIN; INSERT INTO " + table + " VALUES(3); COMMIT; SELECT 1/0"})
		machineExecuteWantError(t, result, err, "DATABASE_FAILURE", "22012", MachineExecutionFailed)
		if f.count(t, 3) != 1 {
			t.Fatal("earlier commit lost")
		}
	})
	t.Run("unfinished and failed local transactions", func(t *testing.T) {
		result, err := f.execute(t, MachineQuery{SQL: "BEGIN; INSERT INTO " + table + " VALUES(4)"})
		machineExecuteWantError(t, result, err, "INCOMPLETE_TRANSACTION", "", MachineExecutionFailed)
		if f.count(t, 4) != 0 {
			t.Fatal("unfinished transaction committed")
		}
		result, err = f.execute(t, MachineQuery{SQL: "BEGIN; INSERT INTO " + table + " VALUES(5); SELECT 1/0; COMMIT"})
		machineExecuteWantError(t, result, err, "DATABASE_FAILURE", "22012", MachineExecutionFailed)
		if f.count(t, 5) != 0 {
			t.Fatal("failed transaction committed")
		}
	})
	t.Run("deferred errors after returning simple and extended", func(t *testing.T) {
		for _, request := range []MachineQuery{
			{SQL: "INSERT INTO " + f.schema + ".deferred VALUES(1),(1) RETURNING v"},
			{SQL: "INSERT INTO " + f.schema + ".deferred VALUES($1::int),($1::int) RETURNING v", Parameters: [][]byte{[]byte("2")}},
		} {
			result, err := f.execute(t, request)
			machineExecuteWantError(t, result, err, "DATABASE_FAILURE", "23505", MachineExecutionFailed)
		}
		var count int
		machineExecutePGError(t, f.observer.QueryRow(context.Background(), "SELECT count(*) FROM "+f.schema+".deferred").Scan(&count))
		if count != 0 {
			t.Fatal("deferred failure retained effects")
		}
	})
	t.Run("parameters and duplicate columns", func(t *testing.T) {
		result := f.must(t, MachineQuery{SQL: "INSERT INTO " + table + " VALUES($1::int) RETURNING v AS x,v AS x,NULL::text", Parameters: [][]byte{[]byte("6")}})
		if f.count(t, 6) != 1 || len(result.Results[0].Columns) != 3 || result.Results[0].Columns[0].Name != result.Results[0].Columns[1].Name || result.Results[0].Rows[0][2] != nil {
			t.Fatal("binding/columns/null")
		}
		result = f.must(t, MachineQuery{SQL: "SELECT FROM generate_series(1,2)"})
		if len(result.Results[0].Columns) != 0 || len(result.Results[0].Rows) != 2 || result.Results[0].Rows[0] == nil {
			t.Fatal("zero-column rows lost")
		}
	})
	t.Run("prepared transactions survive close and explicit recovery", func(t *testing.T) {
		gid := f.prefix + "_prepared"
		// Resolve only this test's GID even if an assertion interrupts recovery.
		t.Cleanup(func() {
			var exists bool
			machineExecutePGError(t, f.observer.QueryRow(context.Background(), "SELECT EXISTS(SELECT 1 FROM pg_prepared_xacts WHERE gid=$1)", gid).Scan(&exists))
			if exists {
				_, err := f.observer.Exec(context.Background(), "ROLLBACK PREPARED '"+gid+"'")
				machineExecutePGError(t, err)
			}
		})
		result := f.must(t, MachineQuery{SQL: "BEGIN; INSERT INTO " + table + " VALUES(7); " + strings.Repeat("SELECT 1;", 260) + " PREPARE TRANSACTION '" + gid + "'"})
		var owner string
		machineExecutePGError(t, f.observer.QueryRow(context.Background(), "SELECT owner::text FROM pg_prepared_xacts WHERE gid=$1", gid).Scan(&owner))
		if !result.Execution.PreparedTransactionsMayRemain || owner != f.role || f.count(t, 7) != 0 {
			t.Fatal("prepared state not disclosed/isolated")
		}
		f.must(t, MachineQuery{SQL: "COMMIT PREPARED '" + gid + "'"})
		if f.count(t, 7) != 1 {
			t.Fatal("prepared commit not observed")
		}
		result = f.must(t, MachineQuery{SQL: "BEGIN; INSERT INTO " + table + " VALUES(8); PREPARE TRANSACTION '" + gid + "'"})
		if !result.Execution.PreparedTransactionsMayRemain {
			t.Fatal("prepared flag missing")
		}
		f.must(t, MachineQuery{SQL: "ROLLBACK PREPARED '" + gid + "'"})
		if f.count(t, 8) != 0 {
			t.Fatal("prepared rollback not observed")
		}
	})
}

func TestMachineExecutePostgresLimits(t *testing.T) {
	f := machineExecutePGFixture(t)
	for i, prefix := range []string{
		"SELECT generate_series(1,20);",
		strings.Repeat("SELECT 1;", 260),
		"SELECT repeat('x',65537);",
		"SELECT repeat(chr(1),65536) FROM generate_series(1,20);",
		"SET client_encoding='LATIN1'; SELECT chr(233);",
	} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			result, err := f.execute(t, MachineQuery{SQL: "BEGIN; " + prefix + fmt.Sprintf(" INSERT INTO %s.effects VALUES(%d); COMMIT", f.schema, i+20), MaxRows: 10})
			if i < 2 {
				machineExecutePGError(t, err)
				if !result.Truncated || len(result.Results) > 256 {
					t.Fatal("display bounds missing")
				}
			} else {
				machineExecuteWantError(t, result, err, "RESOURCE_FAILURE", "", MachineExecutionCompleted)
			}
			if f.count(t, i+20) != 1 {
				t.Fatal("retention interrupted later commit")
			}
		})
	}
	t.Run("protocol ceiling abort is unknown not retention failure", func(t *testing.T) {
		result, err := f.execute(t, MachineQuery{SQL: "SELECT repeat('x',9*1024*1024)"})
		machineExecuteWantError(t, result, err, "DATABASE_FAILURE", "", MachineExecutionUnknown)
	})
}

func TestMachineExecutePostgresPolicyAndMaintenance(t *testing.T) {
	f := machineExecutePGFixture(t)
	table := f.schema + ".effects"
	t.Run("configured role and native privileges", func(t *testing.T) {
		result := f.must(t, MachineQuery{SQL: "SELECT current_user, session_user"})
		if *result.Results[0].Rows[0][0] != f.role || *result.Results[0].Rows[0][1] != f.profile.User {
			t.Fatal("execution was not restricted/configured")
		}
		profile := f.profile
		profile.Role = f.denied
		client := f.open(t, profile)
		result, err := client.Execute(context.Background(), MachineQuery{SQL: "INSERT INTO " + table + " VALUES(30)"})
		machineExecuteWantError(t, result, err, "DATABASE_FAILURE", "42501", MachineExecutionFailed)
		result, err = f.execute(t, MachineQuery{SQL: "RESET ROLE; INSERT INTO " + table + " VALUES(30)"})
		machineExecuteWantError(t, result, err, "DATABASE_FAILURE", "42501", MachineExecutionFailed)
		if f.count(t, 30) != 0 {
			t.Fatal("native role privileges bypassed")
		}
	})
	t.Run("readonly profile and server defaults", func(t *testing.T) {
		profile := f.profile
		profile.ReadOnly = true
		client := f.open(t, profile)
		result, err := client.Execute(context.Background(), MachineQuery{SQL: "INSERT INTO " + table + " VALUES(31)"})
		machineExecuteWantError(t, result, err, "POLICY_DENIED", "", MachineExecutionNotStarted)
		if _, err := client.QueryReadOnly(context.Background(), MachineQuery{SQL: "SELECT 1"}); err != nil {
			t.Fatal(err)
		}
		if _, err := client.QueryReadOnly(context.Background(), MachineQuery{SQL: "DELETE FROM " + table}); err == nil {
			t.Fatal("guarded readonly bypass")
		}
		client = f.open(t, f.dsnProfile(map[string]string{"default_transaction_read_only": "on"}))
		result, err = client.Execute(context.Background(), MachineQuery{SQL: "INSERT INTO " + table + " VALUES(31)"})
		machineExecuteWantError(t, result, err, "DATABASE_FAILURE", "25006", MachineExecutionFailed)
		if f.count(t, 31) != 0 {
			t.Fatal("server readonly was forced off")
		}
	})
	t.Run("maintenance retains native transaction restrictions", func(t *testing.T) {
		f.must(t, MachineQuery{SQL: "VACUUM " + table})
		result, err := f.execute(t, MachineQuery{SQL: "SELECT 1; VACUUM " + table})
		machineExecuteWantError(t, result, err, "DATABASE_FAILURE", "25001", MachineExecutionFailed)
		db := pgx.Identifier{f.prefix + "_db"}.Sanitize()
		t.Cleanup(func() {
			_, err := f.observer.Exec(context.Background(), "DROP DATABASE IF EXISTS "+db)
			machineExecutePGError(t, err)
		})
		f.must(t, MachineQuery{SQL: "CREATE DATABASE " + db})
		f.must(t, MachineQuery{SQL: "DROP DATABASE " + db})
		result, err = f.execute(t, MachineQuery{SQL: "SELECT 1; CREATE DATABASE " + db})
		machineExecuteWantError(t, result, err, "DATABASE_FAILURE", "25001", MachineExecutionFailed)
		profile := f.profile
		profile.Role = f.denied
		result, err = f.open(t, profile).Execute(context.Background(), MachineQuery{SQL: "CREATE DATABASE " + db})
		machineExecuteWantError(t, result, err, "DATABASE_FAILURE", "42501", MachineExecutionFailed)
	})
}

func TestMachineExecutePostgresCancelCommit(t *testing.T) {
	f := machineExecutePGFixture(t)
	f.must(t, MachineQuery{SQL: "CREATE SEQUENCE " + f.schema + ".attempts; CREATE TABLE " + f.schema + ".slow(v int); CREATE FUNCTION " + f.schema + ".pause() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN PERFORM pg_sleep(0.2); RETURN NEW; END$$; CREATE CONSTRAINT TRIGGER slow_commit AFTER INSERT ON " + f.schema + ".slow DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION " + f.schema + ".pause()"})
	client := f.open(t, f.profile)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	result, err := client.Execute(ctx, MachineQuery{SQL: "BEGIN; INSERT INTO " + f.schema + ".slow VALUES(nextval('" + f.prefix + ".attempts')); COMMIT"})
	machineExecuteWantError(t, result, err, "TIMEOUT", "", MachineExecutionUnknown)
	machineExecutePGError(t, client.Close(context.Background()))
	// The independent observer waits past the deferred COMMIT work. Either
	// durability outcome is legal after disconnect; retrying is not.
	time.Sleep(300 * time.Millisecond)
	var attempts, rows int
	machineExecutePGError(t, f.observer.QueryRow(context.Background(), "SELECT last_value FROM "+f.schema+".attempts").Scan(&attempts))
	machineExecutePGError(t, f.observer.QueryRow(context.Background(), "SELECT count(*) FROM "+f.schema+".slow").Scan(&rows))
	if attempts != 1 || rows < 0 || rows > 1 {
		t.Fatal("cancelled COMMIT replayed")
	}
}

func TestMachineCLIBatchAnalysisPostgresDifferential(t *testing.T) {
	f := machineExecutePGFixture(t)
	corpus := []string{
		"SELECT 1", ";; /* ignored */ ; SELECT 1;;-- tail",
		`SELECT 'a;''b' AS "a;""b"; SELECT 2`, `SELECT '\'; SELECT 2`,
		`SELECT E'a\';b', e'\\'; SELECT 2`,
		"SELECT E'first'\n'\\';still literal'; SELECT 2",
		"SELECT E'first'-- comment\r'\\';still literal'; SELECT 2",
		`SELECT B'10', X'FF', N'a;b', U&'a;\0062'; SELECT 2`,
		"DO $$BEGIN PERFORM ';'; END$$; SELECT $a_9$;$other$;$a_9$",
		"SELECT $é_9$a;b$é_9$; SELECT 2",
		"SELECT 1 AS café$tag$, 2 AS 日本語; SELECT 2",
		"/* a /* ; */ b */ SELECT 1; /* ; */ SELECT 2",
		"SELECT 1 -- ; DELETE\n; SELECT 2", "SELECT 1 -- ; DELETE\r; SELECT 2", "SELECT 1 -- ; DELETE\r\n; SELECT 2",
		"CREATE FUNCTION atomic_f() RETURNS int LANGUAGE SQL BEGIN ATOMIC SELECT 1; SELECT 2; END; SELECT atomic_f(); DROP FUNCTION atomic_f()",
		"CREATE FUNCTION atomic_case() RETURNS int LANGUAGE SQL BEGIN ATOMIC SELECT CASE WHEN true THEN CASE WHEN false THEN 1 ELSE 2 END ELSE 3 END; SELECT (CASE WHEN true THEN 1 END); END; SELECT atomic_case(); DROP FUNCTION atomic_case()",
		"CREATE PROCEDURE atomic_p() LANGUAGE SQL BEGIN /*a*/ ATOMIC SELECT 'END;'; END; CALL atomic_p(); DROP PROCEDURE atomic_p()",
		"BEGIN; SELECT 1; END;", `SELECT E'\uD83D\uDE00', E'\U0001F600', E'\xc3\xa9', E'\xZ', E'\101\303\251'`,
		"SELECT 0x_1f, 0o_17, 0b_10, 1_000, .1_5e-2, 1.e+2; SELECT 2",
		strings.Repeat("/*", 256) + ";" + strings.Repeat("*/", 256) + "SELECT 1; SELECT 2",
		"SELECT 1+/* ; */2; SELECT 3+-- ;\r4",
		"SELECT E'\\xc3'\n'\\xa9'; SELECT 2",
		// PostgreSQL scans the entire simple batch before the SET takes effect.
		`SET standard_conforming_strings=off; SELECT '\'; SELECT 2`,
		`SET client_encoding='LATIN1'; SELECT 'ascii;'; SELECT 2`,
	}
	for i, sql := range corpus {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			analysis, err := query.AnalyzeCLIStatements(context.Background(), sql, 0, true, true)
			if err != nil {
				t.Fatalf("analyzer rejected live corpus %d: %v", i, err)
			}
			client := f.open(t, f.dsnProfile(map[string]string{"standard_conforming_strings": "off"}))
			result, err := client.Execute(context.Background(), MachineQuery{SQL: sql, Schema: f.prefix})
			machineExecutePGError(t, err)
			if len(result.Results) != len(analysis.Statements) {
				t.Fatalf("server/analyzer mismatch: server %d analyzer %d", len(result.Results), len(analysis.Statements))
			}
		})
	}
	t.Run("nested routine is parsed but PostgreSQL rejects utility body", func(t *testing.T) {
		sql := "CREATE PROCEDURE nested_p() LANGUAGE SQL BEGIN ATOMIC CREATE FUNCTION nested_f() RETURNS int LANGUAGE SQL BEGIN ATOMIC SELECT 1; END; SELECT 2; END; SELECT 3"
		analysis, err := query.AnalyzeCLIStatements(context.Background(), sql, 0, true, true)
		if err != nil || len(analysis.Statements) != 2 {
			t.Fatal("nested body boundary")
		}
		result, err := f.execute(t, MachineQuery{SQL: sql, Schema: f.prefix})
		machineExecuteWantError(t, result, err, "DATABASE_FAILURE", "0A000", MachineExecutionFailed)
	})
	t.Run("unsupported preflight cannot perform earlier write", func(t *testing.T) {
		for _, suffix := range []string{"COPY " + f.schema + ".effects FROM STDIN", "COPY (SELECT 1) TO STDOUT", "SELECT E'\\xff'", "SELECT 'unterminated"} {
			result, err := f.execute(t, MachineQuery{SQL: "INSERT INTO " + f.schema + ".effects VALUES(99); " + suffix})
			machineExecuteWantError(t, result, err, "UNSUPPORTED_SQL", "", MachineExecutionNotStarted)
		}
		if f.count(t, 99) != 0 {
			t.Fatal("preflight rejection dispatched user SQL")
		}
	})
}
