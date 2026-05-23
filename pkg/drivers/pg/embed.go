package pg

import "embed"

// Embedded pg_catalog introspection SQL. Each file targets Postgres 17 and
// maps to a single domain model: list_databases → models.Database,
// list_schemas → models.Schema, list_tables → models.Table (with
// table_stats supplying EstimatedRows/SizeBytes), list_columns → models.Column,
// list_indexes → models.Index, list_constraints → models.Constraint. Symbols
// are unexported because the executors live in this package.

//go:embed sql/*.sql
var sqlFS embed.FS

//go:embed sql/list_databases.sql
var sqlListDatabases string

//go:embed sql/list_schemas.sql
var sqlListSchemas string

//go:embed sql/list_tables.sql
var sqlListTables string

//go:embed sql/table_stats.sql
var sqlTableStats string

//go:embed sql/list_columns.sql
var sqlListColumns string

//go:embed sql/list_indexes.sql
var sqlListIndexes string

//go:embed sql/list_constraints.sql
var sqlListConstraints string

//go:embed sql/list_foreign_keys.sql
var sqlListForeignKeys string

//go:embed sql/list_inbound_foreign_keys.sql
var sqlListInboundForeignKeys string

//go:embed sql/list_functions.sql
var sqlListFunctions string

//go:embed sql/editability_introspect.sql
var sqlEditabilityIntrospect string
