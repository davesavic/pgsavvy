package editor

import (
	"context"

	"github.com/davesavic/dbsavvy/pkg/gui/editor/sqlcontext"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// SchemaSourceName is the stable Name() for the schema-aware
// completion source. Z1 wiring references this string when
// constructing the engine; tests assert on it too.
const SchemaSourceName = "schema"

// SchemaSourcePriority is the default Priority() the schema source
// declares — the SECONDARY tiebreak in Engine dedupe when two
// Suggestions share the composite Score. It derives from the central
// SchemaSourceBias (completion_source.go) so the schema rank lives in
// ONE place (dbsavvy-ko4m.3, Finding B4) — do not redeclare a separate
// 80 here. Mirrors FunctionSourcePriority = FunctionSourceBias.
const SchemaSourcePriority = SchemaSourceBias

// SchemaProvider returns the current schema name used to resolve
// unqualified `FROM` / `JOIN` table lookups. Empty string is treated
// as "no schema selected" — the source returns empty for table-context
// matches in that case. A schema-qualified reference (`public.users.`)
// carries its own schema in the engine's TableRef/Qualifier and overrides
// this default.
type SchemaProvider func() string

// SchemaSource implements Source by translating the sqlcontext engine's
// cursor-context analysis into table / column suggestions read SYNCHRONOUSLY
// from a background-warmed metadata snapshot (SchemaMetadata). It owns NO
// cache of its own — the store is the single source of truth (ko4m.2 §FROZEN
// DECISION 6). On a column miss it fires a reactive, non-blocking warm via
// TableWarmer and returns immediately with whatever is currently cached
// (possibly empty); when the warm lands, the controller re-triggers completion
// (ko4m.2.3 re-trigger bridge), so the columns appear without an extra
// keystroke.
//
// Detection is entirely engine-backed (sqlcontext.Analyze, ko4m.1.x): no
// regexes, no line stripping. A cursor inside a string/comment yields the zero
// ContextResult, so noise returns empty.
type SchemaSource struct {
	priority int
	meta     SchemaMetadata
	warmer   TableWarmer
	schema   SchemaProvider
}

// NewSchemaSource constructs a SchemaSource over the synchronous metadata
// snapshot (meta), the reactive table warmer (warmer), and the active-schema
// provider (schema). Any argument may be nil — a nil meta/schema causes Suggest
// to return an empty slice, and a nil warmer disables reactive warming (Suggest
// still serves whatever is already cached). The defaults exist so the source
// can be unit-tested in isolation and wired post-construction by the
// orchestrator.
func NewSchemaSource(meta SchemaMetadata, warmer TableWarmer, schema SchemaProvider) *SchemaSource {
	return &SchemaSource{
		priority: SchemaSourcePriority,
		meta:     meta,
		warmer:   warmer,
		schema:   schema,
	}
}

// Name returns the stable source identity.
func (s *SchemaSource) Name() string { return SchemaSourceName }

// Priority returns the source's tiebreak rank for the Engine.
func (s *SchemaSource) Priority() int { return s.priority }

// Suggest returns table or column suggestions based on the cursor context,
// detected by the sqlcontext engine. Reads are SYNCHRONOUS against the snapshot
// store — no driver round-trip, no blocking. Returns an empty slice for any
// failure (no store, no schema, no context, no match, unloaded). Never returns
// nil — callers can range freely.
//
// The engine's ContextResult maps to a suggest helper:
//   - Qualifier.Present              → columns of the resolved table (the
//     `alias.`/`table.` dot context); the resolved Qualifier.Table is
//     preferred, falling back to the raw typed Ident when unresolved.
//   - Expect == ExpectTables         → tables of the current schema (FROM/JOIN).
//   - Expect == ExpectBoth           → ON/USING join condition: in-scope tables
//     plus their columns.
//   - Expect == ExpectColumns        → columns of every in-scope table
//     (SELECT/WHERE), unioned and deduped.
func (s *SchemaSource) Suggest(ctx context.Context, buf *Buffer, pos Position) []Suggestion {
	if buf == nil || s.meta == nil {
		return []Suggestion{}
	}
	res, ok := schemaContextAt(buf, pos)
	if !ok {
		return []Suggestion{}
	}
	prefix := identifierPrefixAt(buf, pos)

	// Dot-qualifier wins: `<ident>.` is an unambiguous single-table column
	// lookup. Prefer the resolved table; fall back to the typed ident so an
	// as-yet-unresolved name is still keyed verbatim into the store. The
	// qualifier carries its own schema (schema-qualified `public.users.`),
	// preferred over the active-schema default.
	if res.Qualifier.Present {
		schema := s.schemaFor(res.Qualifier.Schema)
		table := res.Qualifier.Table
		if table == "" {
			table = res.Qualifier.Ident
		}
		cols := s.suggestColumns(schema, table, prefix)
		// FK-first ranking (ko4m.1.4): in an ON clause (Expect==ExpectBoth),
		// a qualified `o.` column that participates in an FK to another
		// in-scope table ranks first. Outside an ON clause this is a no-op.
		if res.Expect == sqlcontext.ExpectBoth {
			cols = s.fkColumnsFirst(schema, table, res.InScopeTables, cols)
		}
		return cols
	}

	switch res.Expect {
	case sqlcontext.ExpectTables:
		return s.suggestTables(prefix)
	case sqlcontext.ExpectBoth:
		return s.suggestJoinCondition(res.InScopeTables, prefix)
	case sqlcontext.ExpectColumns:
		if len(res.InScopeTables) == 0 {
			return []Suggestion{}
		}
		return s.suggestColumnsMulti(res.InScopeTables, prefix)
	default:
		return []Suggestion{}
	}
}

// suggestTables returns the current schema's tables whose name fuzzily matches
// prefix via editor.Match (empty prefix → all), read synchronously from the
// snapshot. Returns empty when no schema is selected or the schema's table
// names have not been eager-loaded yet.
func (s *SchemaSource) suggestTables(prefix string) []Suggestion {
	schema := s.activeSchema()
	if schema == "" {
		return []Suggestion{}
	}
	names := s.meta.TableNames(schema)
	out := make([]Suggestion, 0, len(names))
	for _, n := range names {
		if n == "" {
			continue
		}
		out = append(out, s.tableSuggestion(schema, n))
	}
	return filterByMatch(out, prefix)
}

// suggestColumns returns columns of (schema,table) whose name fuzzily matches
// prefix via editor.Match (empty → all), read synchronously from the snapshot.
// On a miss (table not warmed) it fires exactly one reactive, non-blocking warm
// and returns empty — the re-trigger bridge re-runs completion when the warm
// lands.
func (s *SchemaSource) suggestColumns(schema, table, prefix string) []Suggestion {
	if schema == "" {
		return []Suggestion{}
	}
	cols, ok := s.meta.Columns(schema, table)
	if !ok {
		s.warm(schema, table)
		return []Suggestion{}
	}
	return filterByMatch(columnSuggestions(cols, s.fkRefs(schema, table)), prefix)
}

// suggestColumnsMulti returns the union of the in-scope tables' columns,
// deduplicated by column name in scope order (a column shared by two tables
// appears once, attributed to the first), then filtered by prefix. Each
// unloaded table fires one reactive warm. Returns empty when no schema is
// selected.
func (s *SchemaSource) suggestColumnsMulti(tables []sqlcontext.TableRef, prefix string) []Suggestion {
	seen := map[string]struct{}{}
	out := []Suggestion{}
	for _, t := range tables {
		if t.Name == "" {
			continue
		}
		schema := s.schemaFor(t.Schema)
		if schema == "" {
			continue
		}
		cols, ok := s.meta.Columns(schema, t.Name)
		if !ok {
			s.warm(schema, t.Name)
			continue
		}
		for _, sg := range columnSuggestions(cols, s.fkRefs(schema, t.Name)) {
			if _, dup := seen[sg.Text]; dup {
				continue
			}
			seen[sg.Text] = struct{}{}
			out = append(out, sg)
		}
	}
	return filterByMatch(out, prefix)
}

// suggestJoinCondition serves the JOIN ... ON / USING position. It offers the
// in-scope tables (so the user can qualify a column via `posts.`) followed by
// those tables' columns, all filtered by the typed prefix. Columns
// participating in an FK between two in-scope tables are ranked first (a small
// Score boost; see fkColumnsFirst). Returns empty when no tables are in scope.
// Duplicate Text across the table and column sets is left for Engine dedupe.
func (s *SchemaSource) suggestJoinCondition(tables []sqlcontext.TableRef, prefix string) []Suggestion {
	if len(tables) == 0 {
		return []Suggestion{}
	}
	out := make([]Suggestion, 0, len(tables))
	for _, t := range tables {
		if t.Name == "" {
			continue
		}
		out = append(out, s.tableSuggestion(s.schemaFor(t.Schema), t.Name))
	}
	out = filterByMatch(out, prefix)

	cols := s.suggestColumnsMulti(tables, prefix)
	cols = s.fkColumnsFirstMulti(tables, cols)
	return append(out, cols...)
}

// fkColumnBoost is the additive Score bump given to a column that participates
// in an FK between two in-scope tables, so it sorts before sibling non-FK
// columns. The Engine sorts by Score descending, so a positive, fixed boost is
// what makes "FK column first" survive the merge sort — input-position ordering
// alone would not, because the table-name suggestions and column suggestions
// from the same schema source otherwise share Score (matchQuality +
// SchemaSourceBias). The boost is intentionally small and additive: it leaves
// the fuzzy matchQuality mechanism (ko4m.3) untouched and only breaks ties
// among schema columns. It is smaller than SchemaSourceBias so FK ranking never
// reorders across sources.
const fkColumnBoost = 5

// fkColumnsFirst boosts the Score of every column of (schema,table) that
// participates in an FK edge with another in-scope table — whether table is the
// referencing side (forward FK: a column of table referencing an in-scope
// RefTable) or the referenced side (reverse FK: another in-scope table's FK
// whose RefTable is table). Boosted columns keep their fuzzy matchQuality and
// remain prefix-filtered (they were already filtered in cols); we only raise
// their Score so the Engine sorts them first among schema columns.
//
// Finding N (cross-schema FK fallback): FK edges are read synchronously via
// s.meta.ForeignKeys. When an edge's owning table is not yet in the snapshot
// (ForeignKeys returns ok==false), it is simply skipped — no FK column is
// promoted for that edge and the normal column list stands. No crash, no
// dropped completion.
//
// Composite FK: every column in ForeignKey.Columns (referencing side) or
// ForeignKey.RefColumns (referenced side) is collected, so all participating
// columns rank first without any positional index assumption.
func (s *SchemaSource) fkColumnsFirst(schema, table string, inScope []sqlcontext.TableRef, cols []Suggestion) []Suggestion {
	if len(cols) == 0 {
		return cols
	}
	fkCols := s.fkColumnSet(schema, table, inScope)
	if len(fkCols) == 0 {
		return cols
	}
	for i := range cols {
		if _, isFK := fkCols[cols[i].Text]; isFK {
			cols[i].Score += fkColumnBoost
		}
	}
	return cols
}

// fkColumnsFirstMulti applies fkColumnsFirst across the unioned column list of
// the in-scope tables (the unqualified ON/USING path). A column is boosted if it
// participates in an FK for ANY in-scope table whose own name matches the
// suggestion's table. Since suggestColumnsMulti dedupes by column name (first
// table wins), we union the FK column sets of all in-scope tables and boost any
// suggestion whose Text is in that union.
func (s *SchemaSource) fkColumnsFirstMulti(tables []sqlcontext.TableRef, cols []Suggestion) []Suggestion {
	if len(cols) == 0 || len(tables) == 0 {
		return cols
	}
	fkCols := map[string]struct{}{}
	for _, t := range tables {
		if t.Name == "" {
			continue
		}
		schema := s.schemaFor(t.Schema)
		if schema == "" {
			continue
		}
		for name := range s.fkColumnSet(schema, t.Name, tables) {
			fkCols[name] = struct{}{}
		}
	}
	if len(fkCols) == 0 {
		return cols
	}
	for i := range cols {
		if _, isFK := fkCols[cols[i].Text]; isFK {
			cols[i].Score += fkColumnBoost
		}
	}
	return cols
}

// fkColumnSet returns the set of column names of (schema,table) that take part
// in an FK linking table to another in-scope table — forward (table references
// an in-scope table) and reverse (an in-scope table references table). Reads FK
// edges synchronously from the snapshot via s.meta.ForeignKeys. An edge whose
// owning table is unloaded (ok==false) is skipped (Finding N fallback). Returns
// an empty set when no in-scope FK edge exists, which makes fkColumnsFirst a
// no-op (normal column list stands).
func (s *SchemaSource) fkColumnSet(schema, table string, inScope []sqlcontext.TableRef) map[string]struct{} {
	out := map[string]struct{}{}

	// Forward: this table's FKs whose RefTable is another in-scope table.
	if fks, ok := s.meta.ForeignKeys(schema, table); ok {
		for _, fk := range fks {
			if !s.refTableInScope(fk.RefSchema, fk.RefTable, schema, inScope) {
				continue
			}
			for _, c := range fk.Columns {
				out[c] = struct{}{}
			}
		}
	}

	// Reverse: another in-scope table's FK whose RefTable is THIS table. The
	// participating columns of THIS table are the FK's RefColumns.
	for _, other := range inScope {
		if other.Name == "" || (other.Name == table && s.schemaFor(other.Schema) == schema) {
			continue
		}
		otherSchema := s.schemaFor(other.Schema)
		if otherSchema == "" {
			continue
		}
		fks, ok := s.meta.ForeignKeys(otherSchema, other.Name)
		if !ok {
			continue
		}
		for _, fk := range fks {
			if !s.sameTable(fk.RefSchema, fk.RefTable, schema, table) {
				continue
			}
			for _, c := range fk.RefColumns {
				out[c] = struct{}{}
			}
		}
	}
	return out
}

// refTableInScope reports whether an FK's referenced (refSchema,refTable) names
// a table that is in scope. An empty refSchema on the edge is resolved against
// ownerSchema (the referencing table's schema) — matching how an unqualified
// in-scope TableRef.Schema resolves to the active schema.
func (s *SchemaSource) refTableInScope(refSchema, refTable, ownerSchema string, inScope []sqlcontext.TableRef) bool {
	for _, t := range inScope {
		if t.Name == "" {
			continue
		}
		if s.sameTable(refSchema, refTable, s.schemaForOwner(t.Schema, ownerSchema), t.Name) {
			return true
		}
	}
	return false
}

// sameTable compares two (schema,table) pairs, resolving an empty edge schema
// against fallbackSchema so an unqualified FK edge matches an in-scope table in
// the same (active) schema.
func (s *SchemaSource) sameTable(aSchema, aTable, bSchema, bTable string) bool {
	if aTable != bTable {
		return false
	}
	as := aSchema
	if as == "" {
		as = bSchema
	}
	return as == bSchema
}

// schemaForOwner resolves an in-scope table's schema: its own qualifier if
// present, else the owner (referencing table) schema as the active-schema
// stand-in.
func (s *SchemaSource) schemaForOwner(refSchema, ownerSchema string) string {
	if refSchema != "" {
		return refSchema
	}
	return ownerSchema
}

// warm fires a reactive, non-blocking table warm. No-op when no warmer is
// wired.
func (s *SchemaSource) warm(schema, table string) {
	if s.warmer == nil {
		return
	}
	s.warmer.WarmTable(schema, table)
}

// columnSuggestions projects a column list into suggestions, populating the
// typed presentation fields (ko4m.4.3, Design D1) from the warmed snapshot:
// Kind=KindColumn, Detail=DataType, IsPrimaryKey/NotNull from the column, and
// FKRef from fkRefs (a column-name → "refschema.reftable.refcol" map for the
// owning table). Text stays the bare column name; Display retains the legacy
// `<name> · <type>` form for the render fallback (Design D6). A column absent
// from fkRefs gets FKRef=="".
func columnSuggestions(cols []models.Column, fkRefs map[string]string) []Suggestion {
	out := make([]Suggestion, 0, len(cols))
	for _, c := range cols {
		if c.Name == "" {
			continue
		}
		out = append(out, Suggestion{
			Text:         c.Name,
			Display:      formatColumnDisplay(c),
			Source:       SchemaSourceName,
			Kind:         KindColumn,
			Detail:       c.DataType,
			IsPrimaryKey: c.IsPrimaryKey,
			NotNull:      !c.Nullable,
			FKRef:        fkRefs[c.Name],
		})
	}
	return out
}

// tableSuggestion builds a table-context suggestion for the bare name n in
// schema. The eager snapshot now carries the relation Kind (ko4m.2 TableEntry),
// so a view/materialized view is marked Kind=KindView; everything else (plain
// table, partitioned table, or an unloaded/absent name with kind "") falls back
// to KindTable. Detail is left empty (a table needs no type detail).
func (s *SchemaSource) tableSuggestion(schema, n string) Suggestion {
	return Suggestion{
		Text:    n,
		Display: n,
		Source:  SchemaSourceName,
		Kind:    mapTableKind(s.meta.TableKind(schema, n)),
	}
}

// mapTableKind maps a models.Table.Kind string (from list_tables.sql's relkind
// CASE) to a completion SuggestionKind. There is no KindMatview, so a
// materialized view is surfaced as KindView (the closest semantic match — both
// are read-only relations). A plain table, a partitioned table, and the ""
// unloaded/unknown sentinel all map to KindTable.
//
//	relkind 'v' -> "view"               -> KindView
//	relkind 'm' -> "materialized_view"  -> KindView
//	relkind 'r' -> "table"              -> KindTable
//	relkind 'p' -> "partitioned_table"  -> KindTable
//	""          (unloaded / absent)     -> KindTable
func mapTableKind(kind string) SuggestionKind {
	switch kind {
	case "view", "materialized_view":
		return KindView
	default:
		return KindTable
	}
}

// fkRefs builds a column-name → "refschema.reftable.refcol" map for (schema,
// table) from the snapshot's FK edges. Composite FKs pair Columns[i] with
// RefColumns[i] positionally (Design: composite FK references the
// positionally-paired ref column). An edge's RefSchema falls back to the
// owning table's schema when the store reports it empty, mirroring how
// fkColumnSet resolves unqualified edges. Returns an empty map when the table
// is unwarmed (ForeignKeys ok==false) so columns get FKRef=="" — never blocks,
// never calls drivers.Session.
func (s *SchemaSource) fkRefs(schema, table string) map[string]string {
	fks, ok := s.meta.ForeignKeys(schema, table)
	if !ok {
		return map[string]string{}
	}
	out := map[string]string{}
	for _, fk := range fks {
		refSchema := fk.RefSchema
		if refSchema == "" {
			refSchema = schema
		}
		for i, col := range fk.Columns {
			if i >= len(fk.RefColumns) {
				break
			}
			if col == "" {
				continue
			}
			if _, dup := out[col]; dup {
				continue
			}
			out[col] = refSchema + "." + fk.RefTable + "." + fk.RefColumns[i]
		}
	}
	return out
}

// schemaFor returns refSchema when the reference carried its own schema
// qualifier (e.g. `public.users.`), else the active-schema default.
func (s *SchemaSource) schemaFor(refSchema string) string {
	if refSchema != "" {
		return refSchema
	}
	return s.activeSchema()
}

// filterByMatch runs editor.Match (fzf-style subsequence matcher, ko4m.3.1)
// against each suggestion's Text. A suggestion is kept iff Match reports ok
// (subsequence that clears the quality floor); a 1-char-overlap junk candidate
// is dropped automatically via ok=false. The composite ranking contract
// (ko4m.3.2) is applied: Score = matchQuality + SchemaSourceBias and Matches =
// the rune offsets into Text that Match flagged.
//
// An empty prefix yields Match("", x) == (true, 0, nil), so every suggestion is
// kept with Score = SchemaSourceBias and Matches = nil — preserving the full
// in-scope list. Input order is preserved; the Engine performs the final
// Score-descending sort.
func filterByMatch(sugs []Suggestion, prefix string) []Suggestion {
	out := make([]Suggestion, 0, len(sugs))
	for _, sg := range sugs {
		ok, quality, positions := Match(prefix, sg.Text)
		if !ok {
			continue
		}
		sg.Score = quality + SchemaSourceBias
		sg.Matches = positions
		out = append(out, sg)
	}
	return out
}

// formatColumnDisplay renders a Column as `<name> · <type>` when DataType is
// non-empty, falling back to just `<name>`.
func formatColumnDisplay(c models.Column) string {
	if c.DataType == "" {
		return c.Name
	}
	return c.Name + " · " + c.DataType
}

// activeSchema safely calls the SchemaProvider; nil provider returns "".
func (s *SchemaSource) activeSchema() string {
	if s.schema == nil {
		return ""
	}
	return s.schema()
}
