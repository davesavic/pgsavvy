//go:build integration && (linux || darwin)

package pg

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/davesavic/pgsavvy/pkg/session"
)

// Reuse the explicitly designated disposable native fixture. Live PG17/18 runs
// and independent shipping-CLI observations belong to T5, not the wire suite.
func TestTemporaryAtomicPostgres(t *testing.T) {
	f := machineExecutePGFixture(t)
	temporaryMachineEnvironment(t)
	id := func(s string) string { return pgx.Identifier{s}.Sanitize() }
	for _, sql := range []string{
		"GRANT USAGE ON SCHEMA " + f.schema + " TO " + id(f.profile.User),
		"GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA " + f.schema + " TO " + id(f.profile.User),
	} {
		_, err := f.observer.Exec(context.Background(), sql)
		machineExecutePGError(t, err)
	}
	port := strconv.Itoa(f.profile.Port)
	passfile := filepath.Join(t.TempDir(), "passfile")
	machineExecutePGError(t, os.WriteFile(passfile, []byte("*:*:*:*:"+f.profile.Password+"\n"), 0o600))
	fields := session.TemporaryTargetFields{Port: port, Database: f.profile.Database, Username: f.profile.User, Passfile: passfile}
	if filepath.IsAbs(f.profile.Host) {
		fields.SocketDir = f.profile.Host
	} else {
		// Only numeric local fixture endpoints are permitted by the public API.
		fields.Host = f.profile.Host
		if f.profile.Host == "localhost" {
			fields.Host = net.IPv4(127, 0, 0, 1).String()
		}
	}
	target, err := session.NewTemporaryTarget(fields)
	machineExecutePGError(t, err)
	execute := func(sql string, maxRows int, callback TemporaryPrecommitValidation) (TemporaryAtomicResult, error) {
		t.Helper()
		client, err := OpenTemporaryMachine(context.Background(), target, MachineOptions{})
		machineExecutePGError(t, err)
		defer func() { machineExecutePGError(t, client.Close(context.Background())) }()
		return client.ExecuteTemporaryAtomic(context.Background(), atomicScript(t, sql, 0, true), nil, TemporaryAtomicOptions{MaxRows: maxRows}, callback)
	}
	for _, tc := range []struct {
		name     string
		sql      string
		value    int
		want     int
		outcome  MachineTransactionOutcome
		callback TemporaryPrecommitValidation
	}{
		{"late-sql-rollback", "INSERT INTO " + f.schema + ".effects VALUES(101); SELECT 1/0", 101, 0, MachineTransactionRolledBack, atomicEncoder},
		{"truncation-commits", "INSERT INTO " + f.schema + ".effects VALUES(102); SELECT v FROM " + f.schema + ".effects UNION ALL SELECT 102", 102, 1, MachineTransactionCommitted, atomicEncoder},
		{"encoder-rollback", "INSERT INTO " + f.schema + ".effects VALUES(103)", 103, 0, MachineTransactionRolledBack, func(context.Context, TemporaryAtomicResult) error { return &MachineError{Code: "RESOURCE_FAILURE"} }},
		{"deferred-commit-rejection", "INSERT INTO " + f.schema + ".effects VALUES(104); INSERT INTO " + f.schema + ".deferred VALUES(1),(1)", 104, 0, MachineTransactionRolledBack, atomicEncoder},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := execute(tc.sql, 1, tc.callback)
			if result.Outcome != tc.outcome || (err == nil) != (tc.outcome == MachineTransactionCommitted) || f.count(t, tc.value) != tc.want {
				t.Fatalf("independent outcome %+v error %v", result, err)
			}
			if tc.name == "truncation-commits" && !result.Truncated {
				t.Fatal("fixture did not exercise requested truncation")
			}
		})
	}
}
