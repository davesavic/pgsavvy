//go:build linux

package cli

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/davesavic/pgsavvy/pkg/drivers/pg"
	"github.com/davesavic/pgsavvy/pkg/models"
)

func TestMachineSchema_Grammar(t *testing.T) {
	for _, args := range [][]string{{}, {"unknown"}, {"list"}, {"tables", "--connection", "x"}, {"describe", "--connection", "x", "--schema", "s"}, {"relationships", "--connection", "x", "--table", "t"}, {"list", "--connection", "x", "--sql", "SELECT 1"}, {"list", "--connection", "x", "--limit", "0"}, {"list", "--connection", "x", "--limit", "1001"}, {"describe", "--connection", "x", "--schema", "s", "--table", "t", "--after", "a"}, {"tables", "--connection", "x", "--schema", "s", "--include-system"}, {"list", "--connection", "x", "--connection", "x"}} {
		loaded := false
		service := InvocationService{Load: func() ([]models.Connection, error) { loaded = true; return nil, nil }}
		if _, err := SchemaCommand(service)(context.Background(), args, IO{}); err == nil || loaded {
			t.Fatalf("invalid schema grammar reached loader: %v", args)
		}
	}
	for _, args := range [][]string{{"--help"}, {"list", "--help"}, {"tables", "--help"}, {"describe", "--help"}, {"relationships", "--help"}} {
		result, err := SchemaCommand(InvocationService{})(context.Background(), args, IO{})
		if err != nil || result.Data == nil {
			t.Fatal("schema help required connection")
		}
	}
}

func TestMachineSchema_Access(t *testing.T) {
	for _, profiles := range [][]models.Connection{{{Name: "x", CLIAccess: models.CLIAccessDisabled}}, {{Name: "x", CLIAccess: models.CLIAccessMetadata}, {Name: "x", CLIAccess: models.CLIAccessRowRead}}, nil} {
		parsed := false
		service := InvocationService{Load: func() ([]models.Connection, error) { return profiles, nil }, Options: pg.MachineOptions{ParseConfig: func(context.Context, models.Connection) (*pgx.ConnConfig, error) { parsed = true; return nil, nil }}}
		if _, err := SchemaCommand(service)(context.Background(), []string{"describe", "--connection", "x", "--schema", `A.Schema`, "--table", `Table".Name`}, IO{}); err == nil || parsed {
			t.Fatal("denied/ambiguous target parsed credentials")
		}
	}
}

func TestMachineSchema_DTOs(t *testing.T) {
	falseValue := false
	raw := pg.MachineDescription{Relation: pg.MachineRelation{Schema: "S", Name: "T", Kind: "table"}, Columns: []pg.MachineCatalogColumn{{Name: "id", Position: 1, DataType: "integer", Nullable: &falseValue}}, Constraints: []pg.MachineConstraint{{Name: "pk", Kind: "p", Definition: "PRIMARY KEY (id)", Columns: []string{"id"}}}, Indexes: []pg.MachineIndex{}}
	public := publicDescription(raw)
	if public.Constraints[0].Kind != "PRIMARY KEY" || raw.Constraints[0].Kind != "p" {
		t.Fatal("constraint labels mutated shared driver values")
	}
	data, err := encode(Envelope{OK: true, Data: public})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if json.Unmarshal(data, &doc) != nil {
		t.Fatal("DTO invalid JSON")
	}
	body := doc["data"].(map[string]any)
	column := body["columns"].([]any)[0].(map[string]any)
	if column["default"] != nil || column["comment"] != nil || column["nullable"] != false || body["indexes"] == nil {
		t.Fatal("null/false/empty-array facts changed")
	}
}

func TestMachineSchema_Pagination(t *testing.T) {
	after := "b.hidden"
	raw := pg.MachineSchemaPage{Items: []pg.MachineSchema{{Name: "a.hidden"}, {Name: "b.hidden"}}, NextAfter: &after, RawComplete: false}
	page, warnings := visibleSchemaPage(raw, false, []string{"*.hidden"})
	if len(page.Items) != 0 || page.Items == nil || page.NextAfter == nil || *page.NextAfter != "b.hidden" || page.RawComplete || len(warnings) != 0 || len(raw.Items) != 2 {
		t.Fatal("visibility erased cursor/completeness or mutated raw page")
	}
	raw.Items = []pg.MachineSchema{{Name: "pg_catalog"}, {Name: "pg_temp_123"}, {Name: "public"}}
	page, _ = visibleSchemaPage(raw, false, nil)
	if len(page.Items) != 1 || page.Items[0].Name != "public" {
		t.Fatal("builtin schemas not hidden")
	}
	page, _ = visibleSchemaPage(raw, true, []string{"pg_catalog"})
	if len(page.Items) != 2 || page.Items[0].Name != "pg_temp_123" {
		t.Fatal("include-system overrode saved profile globs")
	}
	_, warnings = visibleSchemaPage(raw, true, []string{"[", "["})
	if len(warnings) != 1 || warnings[0] != InvalidInput {
		t.Fatal("malformed glob warning leaked raw config or duplicated")
	}
}

func TestMachineSchema_Failures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := SchemaCommand(InvocationService{})(ctx, []string{"list", "--connection", "x"}, IO{})
	if err == nil || result.Data != nil {
		t.Fatal("cancelled schema became success")
	}
	for _, code := range []string{"OBJECT_NOT_FOUND", "RESOURCE_FAILURE", "DATABASE_FAILURE", "TIMEOUT", "POLICY_DENIED"} {
		mapped := PublicMachineError(&pg.MachineError{Code: code})
		if string(mapped.Code) != code {
			t.Fatal("metadata error mapping lost")
		}
	}
}
