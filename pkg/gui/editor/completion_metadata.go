package editor

import "github.com/davesavic/dbsavvy/pkg/models"

// SchemaMetadata is the synchronous, race-safe read surface the completion
// sources query for schema metadata. It is satisfied by the background-warmed
// snapshot store (controllers/helpers/data.SchemaMetadataStore) without the
// editor package importing that package — dependency inversion keeps the
// editor → controllers layering one-directional (the spec's import-cycle
// guard).
//
// Every method is a pure in-memory read: NO network round-trip, NO blocking
// driver call. The (cols,bool) / (fks,bool) shape distinguishes a loaded entry
// from an unloaded one so the source can fire a reactive warm on a miss.
type SchemaMetadata interface {
	// TableNames returns the eager table+view names for schema, or nil if the
	// schema has never been loaded.
	TableNames(schema string) []string
	// TableKind returns the eager Kind string for (schema,name) — the
	// models.Table.Kind value ("table", "view", "materialized_view",
	// "partitioned_table") — or "" when the schema is unloaded or the name is
	// absent. A primitive string read (no shared struct) keeps the editor →
	// controllers import inversion intact.
	TableKind(schema, name string) string
	// Columns returns the column list for (schema,table) and true if loaded;
	// (nil,false) if it has not been warmed yet.
	Columns(schema, table string) ([]models.Column, bool)
	// ForeignKeys returns the foreign-key list for (schema,table) and true if
	// loaded; (nil,false) if it has not been warmed yet.
	ForeignKeys(schema, table string) ([]models.ForeignKey, bool)
	// FunctionNames returns the per-connection function names, or nil if they
	// have never been loaded.
	FunctionNames() []string
}

// TableWarmer is the reactive lazy-load trigger the schema source calls when a
// referenced table's columns are not yet in the snapshot. WarmTable is
// non-blocking and idempotent: it schedules a background load (if not already
// loaded / in-flight / in cooldown) and returns immediately. Satisfied by
// controllers/helpers/data.SchemaWarmer.
type TableWarmer interface {
	WarmTable(schema, table string)
}
