package data

import (
	"strings"
	"sync"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// SchemaMetadataStore is a pure, race-safe, two-tier in-memory cache of schema
// metadata used to back synchronous completion reads (T3) from data warmed by
// background loaders (T2).
//
// Two tiers:
//   - EAGER: per-schema table+view names, and a per-connection function-name
//     list. Cheap to load up front.
//   - LAZY: per (schema,table) column and foreign-key lists, loaded on demand.
//
// Concurrency contract: all methods are safe to call from any goroutine.
// Writers take the write lock and insert a single entry; readers take the read
// lock and return a DEEP defensive copy. A returned slice never aliases store
// state, so callers may freely mutate it.
//
// Deep-copy note (review Finding M): the store never holds *models.Table (Table
// embeds atomic.Int64 and is unsafe to copy) — the eager tier holds a flat
// value projection (TableEntry{Name,Kind}). models.Column is a flat value type,
// so a value copy is already deep. models.ForeignKey carries []string slices, so
// those are cloned explicitly.
type SchemaMetadataStore struct {
	mu sync.RWMutex

	// EAGER tier.
	tables        map[string][]TableEntry // schema -> table+view entries
	functionNames []string                // per-connection function names

	// LAZY tier. Keyed by (schema,table) via tableKey.
	columns     map[string][]models.Column
	foreignKeys map[string][]models.ForeignKey
}

// TableEntry is the eager tier's flat value projection of a relation: its bare
// Name plus its Kind (the models.Table.Kind string — "table", "view",
// "materialized_view", "partitioned_table"). It is a plain value type with no
// atomics, so it is safe to copy by value (review Finding M — unlike
// *models.Table, which the store must never hold).
type TableEntry struct {
	Name string
	Kind string
}

// NewSchemaMetadataStore returns an empty store with all tiers initialised.
func NewSchemaMetadataStore() *SchemaMetadataStore {
	return &SchemaMetadataStore{
		tables:      make(map[string][]TableEntry),
		columns:     make(map[string][]models.Column),
		foreignKeys: make(map[string][]models.ForeignKey),
	}
}

// tableKey builds the composite map key for the lazy tier. Schema and table are
// joined with a NUL separator that cannot appear in a Postgres identifier, so
// distinct (schema,table) pairs can never collide.
func tableKey(schema, table string) string {
	return schema + "\x00" + table
}

// SetTables stores the eager table+view entry list for a schema. A nil or
// empty input is stored as an explicit empty (non-nil) slice so a subsequent
// TableNames read is distinguishable from "never loaded". Entries are a flat
// value type, so the stored slice shares no mutable state with the caller.
func (s *SchemaMetadataStore) SetTables(schema string, entries []TableEntry) {
	cp := copyTableEntries(entries)
	s.mu.Lock()
	s.tables[schema] = cp
	s.mu.Unlock()
}

// TableNames returns a defensive copy of the eager table+view names for a
// schema, or nil if the schema has never been loaded. Names are projected from
// the stored TableEntry list so existing name-only consumers are unchanged.
func (s *SchemaMetadataStore) TableNames(schema string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	src, ok := s.tables[schema]
	if !ok {
		return nil
	}
	out := make([]string, len(src))
	for i, e := range src {
		out[i] = e.Name
	}
	return out
}

// TableKind returns the eager Kind string for (schema,name) — the
// models.Table.Kind value ("table", "view", "materialized_view",
// "partitioned_table"). It returns "" when the schema has not been eager-loaded
// or the name is absent. Lock-safe like the other reads.
func (s *SchemaMetadataStore) TableKind(schema, name string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.tables[schema] {
		if e.Name == name {
			return e.Kind
		}
	}
	return ""
}

// SetFunctionNames stores the per-connection eager function-name list. A nil or
// empty input is stored as an explicit empty (non-nil) slice.
func (s *SchemaMetadataStore) SetFunctionNames(names []string) {
	cp := copyStrings(names)
	s.mu.Lock()
	s.functionNames = cp
	s.mu.Unlock()
}

// FunctionNames returns a defensive copy of the per-connection function names,
// or nil if they have never been loaded.
func (s *SchemaMetadataStore) FunctionNames() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.functionNames == nil {
		return nil
	}
	return copyStrings(s.functionNames)
}

// SetColumns stores the lazy column list for a (schema,table). A nil or empty
// input is stored as an explicit empty (non-nil) slice, so Columns returns
// (empty,true) — distinguishable from the (nil,false) "unloaded" sentinel.
func (s *SchemaMetadataStore) SetColumns(schema, table string, cols []models.Column) {
	cp := copyColumns(cols)
	key := tableKey(schema, table)
	s.mu.Lock()
	s.columns[key] = cp
	s.mu.Unlock()
}

// Columns returns a deep defensive copy of the column list for a (schema,table)
// and true if it has been loaded; (nil,false) if it has not.
func (s *SchemaMetadataStore) Columns(schema, table string) ([]models.Column, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	src, ok := s.columns[tableKey(schema, table)]
	if !ok {
		return nil, false
	}
	return copyColumns(src), true
}

// SetForeignKeys stores the lazy foreign-key list for a (schema,table). A nil
// or empty input is stored as an explicit empty (non-nil) slice.
func (s *SchemaMetadataStore) SetForeignKeys(schema, table string, fks []models.ForeignKey) {
	cp := copyForeignKeys(fks)
	key := tableKey(schema, table)
	s.mu.Lock()
	s.foreignKeys[key] = cp
	s.mu.Unlock()
}

// ForeignKeys returns a deep defensive copy of the foreign-key list for a
// (schema,table) and true if it has been loaded; (nil,false) if it has not.
func (s *SchemaMetadataStore) ForeignKeys(schema, table string) ([]models.ForeignKey, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	src, ok := s.foreignKeys[tableKey(schema, table)]
	if !ok {
		return nil, false
	}
	return copyForeignKeys(src), true
}

// InvalidateTable removes only the lazy-tier (column + FK) entry for the given
// (schema,table). Eager table-name lists and other tables are unaffected. A
// subsequent Columns/ForeignKeys read returns (nil,false).
func (s *SchemaMetadataStore) InvalidateTable(schema, table string) {
	key := tableKey(schema, table)
	s.mu.Lock()
	delete(s.columns, key)
	delete(s.foreignKeys, key)
	s.mu.Unlock()
}

// InvalidateSchema drops every lazy-tier (column + FK) entry for the given
// schema, regardless of table. The eager table-name list for the schema is
// LEFT INTACT (a local DDL renames/adds columns but rarely removes the table
// the user is looking at; the eager list self-heals on the next LoadEager).
// Used on a successful local DDL where parsing the exact target table is
// avoided (decision B: whole-schema invalidation): the
// next WarmTable for any affected table reloads fresh columns/FKs.
//
// A subsequent Columns/ForeignKeys read for any table in schema returns
// (nil,false) until it is re-warmed.
func (s *SchemaMetadataStore) InvalidateSchema(schema string) {
	prefix := schema + "\x00"
	s.mu.Lock()
	for key := range s.columns {
		if strings.HasPrefix(key, prefix) {
			delete(s.columns, key)
		}
	}
	for key := range s.foreignKeys {
		if strings.HasPrefix(key, prefix) {
			delete(s.foreignKeys, key)
		}
	}
	s.mu.Unlock()
}

// Reset clears every tier (table names, function names, columns, FKs). Used on
// reconnect to drop all stale metadata.
func (s *SchemaMetadataStore) Reset() {
	s.mu.Lock()
	s.tables = make(map[string][]TableEntry)
	s.functionNames = nil
	s.columns = make(map[string][]models.Column)
	s.foreignKeys = make(map[string][]models.ForeignKey)
	s.mu.Unlock()
}

// copyTableEntries returns a non-nil copy of src (empty slice for nil/empty
// input). TableEntry is a flat value type, so element assignment fully copies
// each entry.
func copyTableEntries(src []TableEntry) []TableEntry {
	out := make([]TableEntry, len(src))
	copy(out, src)
	return out
}

// copyStrings returns a non-nil copy of src (empty slice for nil/empty input).
func copyStrings(src []string) []string {
	out := make([]string, len(src))
	copy(out, src)
	return out
}

// copyColumns returns a non-nil deep copy of src. models.Column is a flat value
// type, so element assignment fully copies each column.
func copyColumns(src []models.Column) []models.Column {
	out := make([]models.Column, len(src))
	copy(out, src)
	return out
}

// copyForeignKeys returns a non-nil deep copy of src. models.ForeignKey carries
// []string slices (Columns, RefColumns) whose backing arrays must be cloned so
// the snapshot shares no mutable state with the store.
func copyForeignKeys(src []models.ForeignKey) []models.ForeignKey {
	out := make([]models.ForeignKey, len(src))
	for i, fk := range src {
		fk.Columns = copyStrings(fk.Columns)
		fk.RefColumns = copyStrings(fk.RefColumns)
		out[i] = fk
	}
	return out
}
