//go:build integration && linux

package integration_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/davesavic/pgsavvy/pkg/cli"
	"github.com/davesavic/pgsavvy/pkg/models"
)

func firstSchema(t *testing.T, service cli.InvocationService, args ...string) (map[string]any, int) {
	t.Helper()
	return firstDispatch(t, map[string]cli.Command{"schema": cli.SchemaCommand(service)}, append([]string{"schema"}, args...))
}

func firstMetadataService(service cli.InvocationService, hidden []string) cli.InvocationService {
	profiles, _ := service.Load()
	profiles[0].CLIAccess = models.CLIAccessMetadata
	profiles[0].HiddenSchemas = hidden
	service.Load = func() ([]models.Connection, error) { return profiles, nil }
	return service
}

func TestMachineSchema_DTOs_Live(t *testing.T) {
	service, admin, schema := firstMachineFixture(t)
	service = firstMetadataService(service, []string{schema})
	name := `Quoted".Table`
	table := pgx.Identifier{schema, name}.Sanitize()
	if _, err := admin.Exec(context.Background(), "CREATE TABLE "+table+" (id integer PRIMARY KEY, optional text)"); err != nil {
		t.Fatal("quoted table fixture failed")
	}
	doc, exit := firstSchema(t, service, "describe", "--connection", "fixture", "--schema", schema, "--table", name)
	if exit != 0 {
		t.Fatalf("metadata-only describe exit%d", exit)
	}
	data := doc["data"].(map[string]any)
	if data["relation"].(map[string]any)["name"] != name || data["columns"] == nil || data["indexes"] == nil || data["constraints"] == nil || data["rows"] != nil {
		t.Fatal("description shape sampled rows or lost literal identity")
	}
	primary := false
	for _, constraint := range data["constraints"].([]any) {
		if constraint.(map[string]any)["kind"] == "PRIMARY KEY" {
			primary = true
		}
	}
	if !primary {
		t.Fatal("constraint kind not mapped")
	}
	columns := data["columns"].([]any)
	if columns[1].(map[string]any)["default"] != nil || columns[1].(map[string]any)["comment"] != nil || columns[1].(map[string]any)["nullable"] != true {
		t.Fatal("null catalog facts changed")
	}
	doc, exit = firstSchema(t, service, "relationships", "--connection", "fixture", "--schema", schema, "--table", name)
	if exit != 0 {
		t.Fatal("metadata-only relationships failed")
	}
	data = doc["data"].(map[string]any)
	if data["inbound"] == nil || data["outbound"] == nil || len(data["inbound"].([]any)) != 0 || len(data["outbound"].([]any)) != 0 {
		t.Fatal("empty directional arrays changed")
	}
	doc, exit = firstSchema(t, service, "describe", "--connection", "fixture", "--schema", schema, "--table", "missing")
	if exit != cli.ExitDatabase || doc["error"].(map[string]any)["code"] != "OBJECT_NOT_FOUND" || doc["data"] != nil {
		t.Fatal("missing object became successful description")
	}
}

func TestMachineSchema_Pagination_Live(t *testing.T) {
	service, admin, schema := firstMachineFixture(t)
	for _, suffix := range []string{"a_hidden", "b_hidden", "c_visible"} {
		name := schema + suffix
		if _, err := admin.Exec(context.Background(), "CREATE SCHEMA "+pgx.Identifier{name}.Sanitize()); err != nil {
			t.Fatal("schema page fixture failed")
		}
		t.Cleanup(func() {
			if _, err := admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+pgx.Identifier{name}.Sanitize()+" CASCADE"); err != nil {
				t.Error("test-owned paging schema teardown failed")
			}
		})
	}
	service = firstMetadataService(service, []string{schema + "[ab]_hidden"})
	doc, exit := firstSchema(t, service, "list", "--connection", "fixture", "--after", schema, "--limit", "2")
	if exit != 0 {
		t.Fatal("schema page failed")
	}
	data := doc["data"].(map[string]any)
	if data["items"] == nil || len(data["items"].([]any)) != 0 || data["next_after"] != schema+"b_hidden" || data["raw_complete"] != false || doc["result_complete"] != false || doc["truncated"] != true {
		t.Fatal("empty hidden page lost raw cursor/completeness")
	}
	doc, exit = firstSchema(t, service, "list", "--connection", "fixture", "--after", data["next_after"].(string), "--limit", "1")
	if exit != 0 || doc["data"].(map[string]any)["items"].([]any)[0].(map[string]any)["name"] != schema+"c_visible" {
		t.Fatal("visible lookahead skipped")
	}
	contains := func(doc map[string]any, name string) bool {
		for _, item := range doc["data"].(map[string]any)["items"].([]any) {
			if item.(map[string]any)["name"] == name {
				return true
			}
		}
		return false
	}
	doc, _ = firstSchema(t, service, "list", "--connection", "fixture", "--limit", "1000")
	if contains(doc, "pg_catalog") {
		t.Fatal("system schema visible by default")
	}
	doc, _ = firstSchema(t, service, "list", "--connection", "fixture", "--limit", "1000", "--include-system")
	if !contains(doc, "pg_catalog") {
		t.Fatal("include-system omitted system schema")
	}
	service = firstMetadataService(service, []string{"pg_catalog"})
	doc, _ = firstSchema(t, service, "list", "--connection", "fixture", "--limit", "1000", "--include-system")
	if contains(doc, "pg_catalog") {
		t.Fatal("include-system overrode saved glob")
	}
}

func TestMachineSchema_Failures_Live(t *testing.T) {
	service, admin, schema := firstMachineFixture(t)
	service = firstMetadataService(service, nil)
	for _, operation := range []string{"describe", "relationships"} {
		doc, exit := firstSchema(t, service, operation, "--connection", "missing", "--schema", schema, "--table", "protected")
		if exit != cli.ExitUnavailable || doc["data"] != nil {
			t.Fatal("unknown profile fallback occurred")
		}
	}
	profiles, _ := service.Load()
	profiles = append(profiles, profiles[0])
	duplicate := service
	duplicate.Load = func() ([]models.Connection, error) { return profiles, nil }
	if _, exit := firstSchema(t, duplicate, "list", "--connection", "fixture"); exit != cli.ExitUnavailable {
		t.Fatal("ambiguous profile selected")
	}
	if _, err := admin.Exec(context.Background(), "COMMENT ON TABLE "+pgx.Identifier{schema, "protected"}.Sanitize()+" IS '"+strings.Repeat("x", 65537)+"'"); err != nil {
		t.Fatal("oversize catalog fixture failed")
	}
	doc, exit := firstSchema(t, service, "describe", "--connection", "fixture", "--schema", schema, "--table", "protected")
	if exit != cli.ExitResource || doc["data"] != nil {
		t.Fatal("oversize metadata clipped/published")
	}
}

func TestFirstSchemaProcessHelper(t *testing.T) {
	if os.Getenv("PGSAVVY_FIRST_SCHEMA_HELPER") != "1" {
		return
	}
	var profile models.Connection
	var args []string
	if json.Unmarshal([]byte(os.Getenv("PGSAVVY_FIRST_SCHEMA_PROFILE")), &profile) != nil || json.Unmarshal([]byte(os.Getenv("PGSAVVY_FIRST_SCHEMA_ARGS")), &args) != nil {
		os.Exit(2)
	}
	service := cli.InvocationService{Load: func() ([]models.Connection, error) { return []models.Connection{profile}, nil }}
	_, exit := cli.Dispatch(context.Background(), args, cli.IO{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}, map[string]cli.Command{"schema": cli.SchemaCommand(service)})
	os.Exit(exit)
}

func TestMachineSchema_Access_ConcurrentProcesses_Live(t *testing.T) {
	dsn := os.Getenv(envDSN)
	service, _, schema := firstMachineFixture(t)
	service = firstMetadataService(service, nil)
	profiles, _ := service.Load()
	profile, _ := json.Marshal(profiles[0])
	args, _ := json.Marshal([]string{"schema", "describe", "--connection", "fixture", "--schema", schema, "--table", "protected", "--timeout", "2s"})
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFirstSchemaProcessHelper$")
			command.Env = append(os.Environ(), "PGSAVVY_TEST_PG="+dsn, "PGSAVVY_FIRST_SCHEMA_HELPER=1", "PGSAVVY_FIRST_SCHEMA_PROFILE="+string(profile), "PGSAVVY_FIRST_SCHEMA_ARGS="+string(args))
			output, err := command.Output()
			if err != nil {
				t.Errorf("independent schema process failed (%T)", err)
				return
			}
			var doc map[string]any
			if json.Unmarshal(output, &doc) != nil || doc["ok"] != true || doc["connection"] != "fixture" || strings.Contains(string(output), "v17a_synthetic_password") {
				t.Error("independent schema process invalid/leaked result")
			}
		})
	}
	wg.Wait()
}

func TestMachineSchema_Failures_StalledCatalog_Live(t *testing.T) {
	service, admin, schema := firstMachineFixture(t)
	service = firstMetadataService(service, nil)
	transaction, err := admin.Begin(context.Background())
	if err != nil {
		t.Fatal("catalog stall fixture begin failed")
	}
	defer func() { _ = transaction.Rollback(context.Background()) }()
	if _, err := transaction.Exec(context.Background(), "ALTER TABLE "+pgx.Identifier{schema, "protected"}.Sanitize()+" ADD COLUMN pending integer"); err != nil {
		t.Fatal("catalog stall fixture setup failed")
	}
	doc, exit := firstSchema(t, service, "describe", "--connection", "fixture", "--schema", schema, "--table", "protected", "--timeout", "200ms")
	if exit != cli.ExitTimeout || doc["data"] != nil {
		t.Fatal("stalled catalog request did not time out without partial facts")
	}
}
