//go:build linux || darwin

package pg

import (
	"context"
	_ "embed"
	"encoding/json"
	"strconv"
	"unicode/utf8"

	"github.com/davesavic/pgsavvy/pkg/models"
)

//go:embed sql/machine_schemas.sql
var machineSchemasSQL string

//go:embed sql/machine_tables.sql
var machineTablesSQL string

//go:embed sql/machine_describe.sql
var machineDescribeSQL string

//go:embed sql/machine_relationships.sql
var machineRelationshipsSQL string

type MachineMetadataOperation string

const (
	MachineSchemas       MachineMetadataOperation = "schemas"
	MachineTables        MachineMetadataOperation = "tables"
	MachineDescribe      MachineMetadataOperation = "describe"
	MachineRelationships MachineMetadataOperation = "relationships"
)

type (
	MachineMetadataRequest struct {
		Operation            MachineMetadataOperation
		Schema, Table, After string
		Limit                int
	}
	MachineSchema struct {
		Name string `json:"name"`
	}
	MachineRelation struct {
		Schema  string  `json:"schema"`
		Name    string  `json:"name"`
		Kind    string  `json:"kind"`
		Comment *string `json:"comment"`
	}
	MachineCatalogColumn struct {
		Name     string  `json:"name"`
		Position int     `json:"position"`
		DataType string  `json:"data_type"`
		Nullable *bool   `json:"nullable"`
		Default  *string `json:"default"`
		Comment  *string `json:"comment"`
	}
	MachineConstraint struct {
		Name       string   `json:"name"`
		Kind       string   `json:"kind"`
		Definition string   `json:"definition"`
		Columns    []string `json:"columns"`
	}
	MachineIndex struct {
		Name       string `json:"name"`
		Unique     bool   `json:"unique"`
		Primary    bool   `json:"primary"`
		Method     string `json:"method"`
		Definition string `json:"definition"`
	}
	MachineForeignKey struct {
		Name       string   `json:"name"`
		Schema     string   `json:"schema"`
		Table      string   `json:"table"`
		Columns    []string `json:"columns"`
		RefSchema  string   `json:"ref_schema"`
		RefTable   string   `json:"ref_table"`
		RefColumns []string `json:"ref_columns"`
		OnUpdate   string   `json:"on_update"`
		OnDelete   string   `json:"on_delete"`
	}
	MachineSchemaPage struct {
		Items       []MachineSchema `json:"items"`
		NextAfter   *string         `json:"next_after"`
		RawComplete bool            `json:"raw_complete"`
	}
	MachineTablePage struct {
		Schema      string            `json:"schema"`
		Items       []MachineRelation `json:"items"`
		NextAfter   *string           `json:"next_after"`
		RawComplete bool              `json:"raw_complete"`
	}
	MachineDescription struct {
		Relation    MachineRelation        `json:"relation"`
		Columns     []MachineCatalogColumn `json:"columns"`
		Constraints []MachineConstraint    `json:"constraints"`
		Indexes     []MachineIndex         `json:"indexes"`
	}
	MachineRelationshipSet struct {
		Relation MachineRelation     `json:"relation"`
		Inbound  []MachineForeignKey `json:"inbound"`
		Outbound []MachineForeignKey `json:"outbound"`
	}
	MachineMetadata struct {
		Schemas       *MachineSchemaPage
		Tables        *MachineTablePage
		Description   *MachineDescription
		Relationships *MachineRelationshipSet
	}
)

func metadataText(text string) bool      { return len(text) <= 64<<10 && utf8.ValidString(text) }
func metadataOptional(text *string) bool { return text == nil || metadataText(*text) }
func metadataName(text string) bool      { return text != "" && metadataText(text) }
func metadataRelation(relation MachineRelation) bool {
	if !metadataName(relation.Name) || !metadataName(relation.Schema) || !metadataOptional(relation.Comment) {
		return false
	}
	switch relation.Kind {
	case "table", "view", "materialized_view", "partitioned_table":
		return true
	}
	return false
}

func metadataNames(names []string) bool {
	if names == nil || len(names) > 1000 {
		return false
	}
	for _, name := range names {
		if !metadataName(name) {
			return false
		}
	}
	return true
}

func metadataFK(key MachineForeignKey) bool {
	if !metadataName(key.Name) || !metadataName(key.Schema) || !metadataName(key.Table) || !metadataName(key.RefSchema) || !metadataName(key.RefTable) || len(key.Columns) == 0 || len(key.Columns) != len(key.RefColumns) || !metadataNames(key.Columns) || !metadataNames(key.RefColumns) {
		return false
	}
	for _, action := range []string{key.OnUpdate, key.OnDelete} {
		switch action {
		case "NO ACTION", "RESTRICT", "CASCADE", "SET NULL", "SET DEFAULT":
		default:
			return false
		}
	}
	return true
}

func (c *MachineClient) catalog(ctx context.Context, sql string, parameters [][]byte, maxRows int, consume func(string, []byte) error) error {
	reader := c.conn.PgConn().ExecParams(ctx, sql, parameters, nil, nil, nil)
	fail := func(err error) error { c.Abort(); _, _ = reader.Close(); return err }
	rows := 0
	for reader.NextRow() {
		rows++
		values := reader.Values()
		if rows > maxRows || len(values) != 2 || len(values[0]) > 32 || !utf8.Valid(values[0]) || len(values[1]) > 1<<20 || !utf8.Valid(values[1]) {
			return fail(&MachineError{Code: "RESOURCE_FAILURE"})
		}
		if err := consume(string(values[0]), values[1]); err != nil {
			return fail(err)
		}
	}
	_, err := reader.Close()
	return err
}

func (c *MachineClient) Inspect(ctx context.Context, request MachineMetadataRequest) (MachineMetadata, error) {
	if c.access != models.CLIAccessMetadata && c.access != models.CLIAccessRowRead {
		return MachineMetadata{}, &MachineError{Code: "POLICY_DENIED"}
	}
	if !metadataText(request.Schema) || !metadataText(request.Table) || !metadataText(request.After) {
		return MachineMetadata{}, &MachineError{Code: "INVALID_INPUT"}
	}
	limit := request.Limit
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 1000 {
		return MachineMetadata{}, &MachineError{Code: "INVALID_INPUT"}
	}
	result := MachineMetadata{}
	var sql string
	var parameters [][]byte
	maxRows := limit + 1
	switch request.Operation {
	case MachineSchemas:
		if request.Schema != "" || request.Table != "" {
			return result, &MachineError{Code: "INVALID_INPUT"}
		}
		result.Schemas = &MachineSchemaPage{Items: []MachineSchema{}, RawComplete: true}
		sql = machineSchemasSQL
		parameters = [][]byte{[]byte(request.After), []byte(strconv.Itoa(limit + 1))}
	case MachineTables:
		if request.Schema == "" || request.Table != "" {
			return result, &MachineError{Code: "INVALID_INPUT"}
		}
		result.Tables = &MachineTablePage{Schema: request.Schema, Items: []MachineRelation{}, RawComplete: true}
		sql = machineTablesSQL
		parameters = [][]byte{[]byte(request.Schema), []byte(request.After), []byte(strconv.Itoa(limit + 1))}
		maxRows = limit + 2
	case MachineDescribe, MachineRelationships:
		if request.Schema == "" || request.Table == "" || request.After != "" || request.Limit != 0 {
			return result, &MachineError{Code: "INVALID_INPUT"}
		}
		parameters = [][]byte{[]byte(request.Schema), []byte(request.Table)}
		if request.Operation == MachineDescribe {
			result.Description = &MachineDescription{Columns: []MachineCatalogColumn{}, Constraints: []MachineConstraint{}, Indexes: []MachineIndex{}}
			sql = machineDescribeSQL
			maxRows = 3004
		} else {
			result.Relationships = &MachineRelationshipSet{Inbound: []MachineForeignKey{}, Outbound: []MachineForeignKey{}}
			sql = machineRelationshipsSQL
			maxRows = 2003
		}
	default:
		return result, &MachineError{Code: "INVALID_INPUT"}
	}
	counts := map[string]int{}
	budget := 0
	found := false
	err := c.withReadOnly(ctx, true, "pg_catalog", func(ctx context.Context) error {
		return c.catalog(ctx, sql, parameters, maxRows, func(section string, raw []byte) error {
			resource := func() error { return &MachineError{Code: "RESOURCE_FAILURE"} }
			malformed := func() error { return &MachineError{Code: "DATABASE_FAILURE"} }
			counts[section]++
			if section != "schemas" && section != "tables" && counts[section] > 1000 {
				return resource()
			}
			var value any
			var retain func()
			switch section {
			case "schema":
				if result.Tables == nil || found {
					return malformed()
				}
				var item MachineSchema
				if json.Unmarshal(raw, &item) != nil || item.Name != request.Schema {
					return malformed()
				}
				retain = func() { found = true }
				value = item
			case "schemas":
				if result.Schemas == nil {
					return malformed()
				}
				var item MachineSchema
				if json.Unmarshal(raw, &item) != nil || !metadataName(item.Name) {
					return malformed()
				}
				if len(result.Schemas.Items) < limit {
					retain = func() { result.Schemas.Items = append(result.Schemas.Items, item) }
				} else {
					result.Schemas.RawComplete = false
					last := result.Schemas.Items[len(result.Schemas.Items)-1].Name
					result.Schemas.NextAfter = &last
					return nil
				}
				value = item
			case "tables":
				if result.Tables == nil {
					return malformed()
				}
				var item MachineRelation
				if json.Unmarshal(raw, &item) != nil || !metadataRelation(item) {
					return resource()
				}
				if len(result.Tables.Items) < limit {
					retain = func() { result.Tables.Items = append(result.Tables.Items, item) }
				} else {
					result.Tables.RawComplete = false
					last := result.Tables.Items[len(result.Tables.Items)-1].Name
					result.Tables.NextAfter = &last
					return nil
				}
				value = item
			case "relation":
				var item MachineRelation
				if found || json.Unmarshal(raw, &item) != nil {
					return malformed()
				}
				if !metadataRelation(item) {
					return resource()
				}
				if result.Description != nil {
					retain = func() { result.Description.Relation = item; found = true }
				} else if result.Relationships != nil {
					retain = func() { result.Relationships.Relation = item; found = true }
				} else {
					return malformed()
				}
				value = item
			case "columns":
				if result.Description == nil {
					return malformed()
				}
				var item MachineCatalogColumn
				if json.Unmarshal(raw, &item) != nil || !metadataName(item.Name) || !metadataText(item.DataType) || item.DataType == "" || item.Position < 1 || item.Nullable == nil {
					return malformed()
				}
				if !metadataOptional(item.Default) || !metadataOptional(item.Comment) {
					return resource()
				}
				retain = func() { result.Description.Columns = append(result.Description.Columns, item) }
				value = item
			case "constraints":
				if result.Description == nil {
					return malformed()
				}
				var item MachineConstraint
				if json.Unmarshal(raw, &item) != nil || !metadataName(item.Name) || !metadataName(item.Kind) || !metadataName(item.Definition) || !metadataNames(item.Columns) {
					return resource()
				}
				retain = func() { result.Description.Constraints = append(result.Description.Constraints, item) }
				value = item
			case "indexes":
				if result.Description == nil {
					return malformed()
				}
				var item MachineIndex
				if json.Unmarshal(raw, &item) != nil || !metadataName(item.Name) || !metadataName(item.Method) || !metadataName(item.Definition) {
					return resource()
				}
				retain = func() { result.Description.Indexes = append(result.Description.Indexes, item) }
				value = item
			case "inbound", "outbound":
				if result.Relationships == nil {
					return malformed()
				}
				var item MachineForeignKey
				if json.Unmarshal(raw, &item) != nil || !metadataFK(item) {
					return resource()
				}
				if section == "inbound" {
					retain = func() { result.Relationships.Inbound = append(result.Relationships.Inbound, item) }
				} else {
					retain = func() { result.Relationships.Outbound = append(result.Relationships.Outbound, item) }
				}
				value = item
			default:
				return malformed()
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				return resource()
			}
			budget += len(encoded)
			if budget > 1<<20 {
				return resource()
			}
			retain()
			return nil
		})
	})
	if err != nil {
		return MachineMetadata{}, err
	}
	if (result.Tables != nil || result.Description != nil || result.Relationships != nil) && !found {
		return MachineMetadata{}, &MachineError{Code: "OBJECT_NOT_FOUND"}
	}
	return result, nil
}
