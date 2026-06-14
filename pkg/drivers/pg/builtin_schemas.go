package pg

// BuiltinHiddenSchemas lists the Postgres-managed schemas that the schemas
// helper hides by default in the TUI's left rail. Entries are matched against
// schema names literally except those ending in "_*", which match any schema
// whose name starts with the literal prefix (the per-backend pg_temp_NNN /
// pg_toast_temp_NNN namespaces).
//
// Source: DESIGN.md §15.9. Promotion plan: when a second engine lands this
// will move behind drivers.Driver.BuiltinHiddenObjects() so the schemas
// helper does not import a concrete driver package.
var BuiltinHiddenSchemas = []string{
	"pg_catalog",
	"information_schema",
	"pg_toast",
	"pg_temp_*",
	"pg_toast_temp_*",
}
