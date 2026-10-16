//go:build linux

package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/davesavic/pgsavvy/pkg/models"
)

func metadataWireRow(t *testing.T, section string, value any) [][]byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return [][]byte{[]byte(section), encoded}
}

func metadataWire(t *testing.T, operation MachineMetadataOperation, rows [][][]byte, configure ...func(*machineQueryScript)) (*MachineClient, *machineQueryScript) {
	t.Helper()
	match := "pg_catalog.pg_class"
	if operation == MachineSchemas || operation == MachineTables {
		match = "pg_catalog.pg_namespace"
	}
	script := &machineQueryScript{fields: []pgproto3.FieldDescription{{Name: []byte("section"), DataTypeOID: 25}, {Name: []byte("item"), DataTypeOID: 3802}}, rows: rows, queryMatch: match}
	for _, apply := range configure {
		apply(script)
	}
	return machineQueryWire(t, script), script
}

func metadataRelationFixture() MachineRelation {
	return MachineRelation{Schema: "Exact.Schema", Name: "Exact.Table", Kind: "table"}
}

func TestMachineMetadata_Paging(t *testing.T) {
	rows := [][][]byte{}
	for i := 0; i < 101; i++ {
		rows = append(rows, metadataWireRow(t, "schemas", MachineSchema{Name: fmt.Sprintf("n%04d", i)}))
	}
	client, script := metadataWire(t, MachineSchemas, rows)
	result, err := client.Inspect(context.Background(), MachineMetadataRequest{Operation: MachineSchemas})
	if err != nil || len(result.Schemas.Items) != 100 || result.Schemas.RawComplete || result.Schemas.NextAfter == nil || *result.Schemas.NextAfter != "n0099" {
		t.Fatalf("default raw-page boundary: %+v %v", result.Schemas, err)
	}
	script.mu.Lock()
	if len(script.parameters) != 2 || string(script.parameters[1]) != "101" {
		t.Error("SQL limit+1 was not bound")
	}
	script.mu.Unlock()
	rows = [][][]byte{metadataWireRow(t, "schema", MachineSchema{Name: "Exact.Schema"})}
	for _, name := range []string{"a.hidden", "b.hidden", "c.visible"} {
		item := metadataRelationFixture()
		item.Name = name
		rows = append(rows, metadataWireRow(t, "tables", item))
	}
	client, _ = metadataWire(t, MachineTables, rows)
	result, err = client.Inspect(context.Background(), MachineMetadataRequest{Operation: MachineTables, Schema: "Exact.Schema", Limit: 2})
	if err != nil || len(result.Tables.Items) != 2 || result.Tables.RawComplete || *result.Tables.NextAfter != "b.hidden" {
		t.Fatal("lookahead included in continuation")
	}
	// Visibility is the adapter's concern: even hiding both retained entries
	// must leave this raw cursor pointing before the visible lookahead.
	rows = [][][]byte{metadataWireRow(t, "schema", MachineSchema{Name: "Exact.Schema"})}
	item := metadataRelationFixture()
	item.Name = "c.visible"
	rows = append(rows, metadataWireRow(t, "tables", item))
	client, script = metadataWire(t, MachineTables, rows)
	result, err = client.Inspect(context.Background(), MachineMetadataRequest{Operation: MachineTables, Schema: "Exact.Schema", After: "b.hidden", Limit: 2})
	if err != nil || len(result.Tables.Items) != 1 || result.Tables.Items[0].Name != "c.visible" || !result.Tables.RawComplete || result.Tables.NextAfter != nil {
		t.Fatal("visible lookahead skipped")
	}
	script.mu.Lock()
	if string(script.parameters[1]) != "b.hidden" {
		t.Error("cursor not bound literally")
	}
	script.mu.Unlock()
}

func TestMachineMetadata_EmbeddedSQL(t *testing.T) {
	for name, query := range map[string]string{"machine_schemas.sql": machineSchemasSQL, "machine_tables.sql": machineTablesSQL, "machine_describe.sql": machineDescribeSQL, "machine_relationships.sql": machineRelationshipsSQL} {
		data, err := sqlFS.ReadFile("sql/" + name)
		if err != nil || len(data) == 0 || string(data) != query {
			t.Fatalf("machine catalog executor is not using its exact embedded asset %s", name)
		}
	}
}

func TestMachineMetadata_Describe(t *testing.T) {
	nullable := false
	rows := [][][]byte{metadataWireRow(t, "relation", metadataRelationFixture()), metadataWireRow(t, "columns", MachineCatalogColumn{Name: "Value.With.Dot", Position: 3, DataType: "numeric(30,10)", Nullable: &nullable})}
	client, script := metadataWire(t, MachineDescribe, rows)
	result, err := client.Inspect(context.Background(), MachineMetadataRequest{Operation: MachineDescribe, Schema: "Exact.Schema", Table: "Exact.Table"})
	if err != nil || result.Description == nil || len(result.Description.Columns) != 1 {
		t.Fatalf("describe: %v", err)
	}
	column := result.Description.Columns[0]
	if *column.Nullable || column.Default != nil || column.Comment != nil || column.Position != 3 || column.Name != "Value.With.Dot" || result.Description.Constraints == nil || result.Description.Indexes == nil {
		t.Fatal("description fabricated defaults/nulls/arrays")
	}
	script.mu.Lock()
	if string(script.parameters[0]) != "Exact.Schema" || string(script.parameters[1]) != "Exact.Table" {
		t.Error("literal identifiers split")
	}
	script.mu.Unlock()
	for _, sql := range []string{machineDescribeSQL, machineRelationshipsSQL} {
		if strings.Count(sql, "LIMIT 1001") < 4 || strings.Contains(strings.ToUpper(sql), "COUNT(") {
			t.Fatal("catalog source/aggregate bounds missing")
		}
	}
	for _, sql := range []string{machineSchemasSQL, machineTablesSQL} {
		if !strings.Contains(sql, `COLLATE "C"`) || !strings.Contains(sql, "LIMIT $") {
			t.Fatal("paging ordering/SQL bound missing")
		}
	}
}

func TestMachineMetadata_Indexes(t *testing.T) {
	definition := `CREATE UNIQUE INDEX "Expr" ON "Exact.Schema"."Exact.Table" USING btree (lower(value)) INCLUDE (id) WHERE (id > 0)`
	rows := [][][]byte{metadataWireRow(t, "relation", metadataRelationFixture()), metadataWireRow(t, "indexes", MachineIndex{Name: "Expr", Unique: true, Method: "btree", Definition: definition})}
	client, _ := metadataWire(t, MachineDescribe, rows)
	result, err := client.Inspect(context.Background(), MachineMetadataRequest{Operation: MachineDescribe, Schema: "Exact.Schema", Table: "Exact.Table"})
	if err != nil || len(result.Description.Indexes) != 1 || result.Description.Indexes[0].Definition != definition {
		t.Fatal("index semantics/text lost")
	}
}

func TestMachineMetadata_Relationships(t *testing.T) {
	key := MachineForeignKey{Name: "Self.FK", Schema: "Exact.Schema", Table: "Exact.Table", Columns: []string{"y", "x"}, RefSchema: "Other.Schema", RefTable: "Ref.Table", RefColumns: []string{"b", "a"}, OnUpdate: "SET NULL", OnDelete: "CASCADE"}
	rows := [][][]byte{metadataWireRow(t, "relation", metadataRelationFixture()), metadataWireRow(t, "inbound", key), metadataWireRow(t, "outbound", key)}
	client, _ := metadataWire(t, MachineRelationships, rows)
	result, err := client.Inspect(context.Background(), MachineMetadataRequest{Operation: MachineRelationships, Schema: "Exact.Schema", Table: "Exact.Table"})
	if err != nil || len(result.Relationships.Inbound) != 1 || len(result.Relationships.Outbound) != 1 || result.Relationships.Outbound[0].Columns[0] != "y" || result.Relationships.Outbound[0].RefColumns[0] != "b" || result.Relationships.Inbound[0].RefSchema != "Other.Schema" {
		t.Fatal("directional composite FK pairing lost")
	}
}

func TestMachineMetadata_Failures(t *testing.T) {
	client := &MachineClient{access: models.CLIAccessMetadata}
	for _, request := range []MachineMetadataRequest{{Operation: "arbitrary SQL"}, {Operation: MachineSchemas, Limit: -1}, {Operation: MachineSchemas, Limit: 1001}, {Operation: MachineTables}, {Operation: MachineDescribe, Schema: "x"}, {Operation: MachineDescribe, Schema: "x", Table: "y", After: "z"}} {
		if _, err := client.Inspect(context.Background(), request); err == nil {
			t.Fatal("invalid metadata request reached connection")
		}
	}
	client = &MachineClient{}
	if _, err := client.Inspect(context.Background(), MachineMetadataRequest{Operation: MachineSchemas}); err == nil {
		t.Fatal("disabled metadata client inspected catalog")
	}
	client, _ = metadataWire(t, MachineDescribe, nil)
	result, err := client.Inspect(context.Background(), MachineMetadataRequest{Operation: MachineDescribe, Schema: "Exact.Schema", Table: "missing"})
	var safe *MachineError
	if !errors.As(err, &safe) || safe.Code != "OBJECT_NOT_FOUND" || result.Description != nil {
		t.Fatal("missing object became empty description")
	}
	rows := [][][]byte{metadataWireRow(t, "relation", metadataRelationFixture())}
	client, _ = metadataWire(t, MachineDescribe, rows, func(script *machineQueryScript) { script.terminalError = true; script.terminalCode = "42501" })
	result, err = client.Inspect(context.Background(), MachineMetadataRequest{Operation: MachineDescribe, Schema: "Exact.Schema", Table: "Exact.Table"})
	if !errors.As(err, &safe) || safe.SQLState != "42501" || result.Description != nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatal("catalog denial became partial success")
	}
	client, _ = metadataWire(t, MachineDescribe, rows, func(script *machineQueryScript) { script.stallTail = true })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	result, err = client.Inspect(ctx, MachineMetadataRequest{Operation: MachineDescribe, Schema: "Exact.Schema", Table: "Exact.Table"})
	if !errors.As(err, &safe) || safe.Code != "TIMEOUT" || result.Description != nil {
		t.Fatal("cancelled metadata became partial success")
	}
}

func TestMachineMetadata_Failures_Aggregates(t *testing.T) {
	for _, section := range []string{"columns", "constraints", "indexes", "inbound", "outbound"} {
		t.Run(section, func(t *testing.T) {
			rows := [][][]byte{metadataWireRow(t, "relation", metadataRelationFixture())}
			operation := MachineDescribe
			for i := 0; i < 1001; i++ {
				name := fmt.Sprintf("n%04d", i)
				var item any
				nullable := true
				switch section {
				case "columns":
					item = MachineCatalogColumn{Name: name, Position: i + 1, DataType: "text", Nullable: &nullable}
				case "constraints":
					item = MachineConstraint{Name: name, Kind: "c", Definition: "CHECK (true)", Columns: []string{}}
				case "indexes":
					item = MachineIndex{Name: name, Method: "btree", Definition: "CREATE INDEX x ON y (z)"}
				default:
					operation = MachineRelationships
					item = MachineForeignKey{Name: name, Schema: "s", Table: "t", Columns: []string{"x"}, RefSchema: "s", RefTable: "r", RefColumns: []string{"y"}, OnUpdate: "NO ACTION", OnDelete: "NO ACTION"}
				}
				rows = append(rows, metadataWireRow(t, section, item))
			}
			client, _ := metadataWire(t, operation, rows)
			result, err := client.Inspect(context.Background(), MachineMetadataRequest{Operation: operation, Schema: "Exact.Schema", Table: "Exact.Table"})
			var safe *MachineError
			if !errors.As(err, &safe) || safe.Code != "RESOURCE_FAILURE" || result.Description != nil || result.Relationships != nil {
				t.Fatalf("%s overflow became complete facts: %v", section, err)
			}
		})
	}
	comment := strings.Repeat("x", (64<<10)+1)
	relation := metadataRelationFixture()
	relation.Comment = &comment
	client, _ := metadataWire(t, MachineDescribe, [][][]byte{metadataWireRow(t, "relation", relation)})
	if _, err := client.Inspect(context.Background(), MachineMetadataRequest{Operation: MachineDescribe, Schema: "Exact.Schema", Table: "Exact.Table"}); err == nil {
		t.Fatal("oversized catalog value accepted")
	}
}

func TestMachineMetadata_Failures_CumulativeBudget(t *testing.T) {
	for _, count := range []int{400, 600} {
		rows := [][][]byte{metadataWireRow(t, "relation", metadataRelationFixture())}
		nullable := true
		comment := strings.Repeat("x", 2000)
		for i := 0; i < count; i++ {
			rows = append(rows, metadataWireRow(t, "columns", MachineCatalogColumn{Name: fmt.Sprintf("c%d", i), Position: i + 1, DataType: "text", Nullable: &nullable, Comment: &comment}))
		}
		client, _ := metadataWire(t, MachineDescribe, rows)
		result, err := client.Inspect(context.Background(), MachineMetadataRequest{Operation: MachineDescribe, Schema: "Exact.Schema", Table: "Exact.Table"})
		if count == 400 {
			if err != nil || len(result.Description.Columns) != count {
				t.Fatal("bounded metadata unexpectedly rejected")
			}
		} else {
			var safe *MachineError
			if !errors.As(err, &safe) || safe.Code != "RESOURCE_FAILURE" || result.Description != nil {
				t.Fatal("many individually valid facts exceeded retained budget")
			}
		}
	}
}
