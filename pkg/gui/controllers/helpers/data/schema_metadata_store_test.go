package data

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/davesavic/dbsavvy/pkg/models"
)

func sampleColumns() []models.Column {
	return []models.Column{
		{Name: "id", DataType: "integer", IsPrimaryKey: true, Position: 1},
		{Name: "email", DataType: "text", Nullable: true, Position: 2},
	}
}

func sampleFKs() []models.ForeignKey {
	return []models.ForeignKey{
		{
			Name:       "fk_orders_user",
			Schema:     "public",
			Table:      "orders",
			Columns:    []string{"user_id"},
			RefSchema:  "public",
			RefTable:   "users",
			RefColumns: []string{"id"},
			OnDelete:   "CASCADE",
		},
	}
}

func TestSchemaMetadataStore_EmptyStateReads(t *testing.T) {
	s := NewSchemaMetadataStore()

	require.Nil(t, s.TableNames("public"), "unloaded schema -> nil names")
	require.Nil(t, s.FunctionNames(), "unloaded functions -> nil")

	cols, ok := s.Columns("public", "users")
	require.False(t, ok)
	require.Nil(t, cols)

	fks, ok := s.ForeignKeys("public", "orders")
	require.False(t, ok)
	require.Nil(t, fks)
}

func sampleTableEntries() []TableEntry {
	return []TableEntry{
		{Name: "users", Kind: "table"},
		{Name: "orders", Kind: "table"},
	}
}

func TestSchemaMetadataStore_TableNamesRoundTrip(t *testing.T) {
	s := NewSchemaMetadataStore()
	s.SetTables("public", sampleTableEntries())

	require.Equal(t, []string{"users", "orders"}, s.TableNames("public"))
	require.Nil(t, s.TableNames("other"), "untouched schema stays unloaded")
}

// TableKind reads the per-name eager kind, distinguishing views from tables;
// an unloaded schema or an absent name returns "".
func TestSchemaMetadataStore_TableKind(t *testing.T) {
	s := NewSchemaMetadataStore()
	s.SetTables("public", []TableEntry{
		{Name: "users", Kind: "table"},
		{Name: "active_users", Kind: "view"},
		{Name: "user_stats", Kind: "materialized_view"},
		{Name: "events", Kind: "partitioned_table"},
	})

	require.Equal(t, "table", s.TableKind("public", "users"))
	require.Equal(t, "view", s.TableKind("public", "active_users"))
	require.Equal(t, "materialized_view", s.TableKind("public", "user_stats"))
	require.Equal(t, "partitioned_table", s.TableKind("public", "events"))
	require.Equal(t, "", s.TableKind("public", "missing"), "absent name -> empty")
	require.Equal(t, "", s.TableKind("other", "users"), "unloaded schema -> empty")
}

func TestSchemaMetadataStore_FunctionNamesRoundTrip(t *testing.T) {
	s := NewSchemaMetadataStore()
	s.SetFunctionNames([]string{"now", "gen_random_uuid"})
	require.Equal(t, []string{"now", "gen_random_uuid"}, s.FunctionNames())
}

func TestSchemaMetadataStore_ColumnsLoadedFlag(t *testing.T) {
	s := NewSchemaMetadataStore()

	cols, ok := s.Columns("public", "users")
	require.False(t, ok)
	require.Nil(t, cols)

	s.SetColumns("public", "users", sampleColumns())

	got, ok := s.Columns("public", "users")
	require.True(t, ok)
	require.Equal(t, sampleColumns(), got)
}

func TestSchemaMetadataStore_ForeignKeysLoadedFlag(t *testing.T) {
	s := NewSchemaMetadataStore()

	fks, ok := s.ForeignKeys("public", "orders")
	require.False(t, ok)
	require.Nil(t, fks)

	s.SetForeignKeys("public", "orders", sampleFKs())

	got, ok := s.ForeignKeys("public", "orders")
	require.True(t, ok)
	require.Equal(t, sampleFKs(), got)
}

// Boundary: empty (non-nil) input is retrievable as (empty,true), distinct from
// the unloaded (nil,false) sentinel.
func TestSchemaMetadataStore_EmptyInputIsLoaded(t *testing.T) {
	s := NewSchemaMetadataStore()

	s.SetColumns("public", "empty_tbl", []models.Column{})
	cols, ok := s.Columns("public", "empty_tbl")
	require.True(t, ok, "empty set must be loaded")
	require.NotNil(t, cols)
	require.Len(t, cols, 0)

	s.SetForeignKeys("public", "empty_tbl", nil)
	fks, ok := s.ForeignKeys("public", "empty_tbl")
	require.True(t, ok, "nil set is stored as explicit empty")
	require.NotNil(t, fks)
	require.Len(t, fks, 0)

	s.SetTables("emptyschema", nil)
	names := s.TableNames("emptyschema")
	require.NotNil(t, names)
	require.Len(t, names, 0)

	s.SetFunctionNames(nil)
	funcs := s.FunctionNames()
	require.NotNil(t, funcs)
	require.Len(t, funcs, 0)
}

// Deep-copy isolation: mutating a returned slice (and its FK sub-slices) must
// not change a fresh read. Covers review Finding M.
func TestSchemaMetadataStore_ReadsAreDeepCopies(t *testing.T) {
	s := NewSchemaMetadataStore()
	s.SetColumns("public", "users", sampleColumns())
	s.SetForeignKeys("public", "orders", sampleFKs())
	s.SetTables("public", sampleTableEntries())
	s.SetFunctionNames([]string{"now"})

	// Mutate every returned slice/element.
	cols, _ := s.Columns("public", "users")
	cols[0].Name = "MUTATED"
	cols[0].DataType = "MUTATED"

	fks, _ := s.ForeignKeys("public", "orders")
	fks[0].Name = "MUTATED"
	fks[0].Columns[0] = "MUTATED"    // mutate FK sub-slice element
	fks[0].RefColumns[0] = "MUTATED" // mutate FK sub-slice element

	names := s.TableNames("public")
	names[0] = "MUTATED"

	funcs := s.FunctionNames()
	funcs[0] = "MUTATED"

	// Fresh reads must be pristine.
	require.Equal(t, sampleColumns(), mustCols(t, s, "public", "users"))
	require.Equal(t, sampleFKs(), mustFKs(t, s, "public", "orders"))
	require.Equal(t, []string{"users", "orders"}, s.TableNames("public"))
	require.Equal(t, []string{"now"}, s.FunctionNames())
}

// Writes must not alias the caller's input either: mutating the slice passed to
// SetX after the call must not change stored state.
func TestSchemaMetadataStore_WritesCopyInput(t *testing.T) {
	s := NewSchemaMetadataStore()

	cols := sampleColumns()
	s.SetColumns("public", "users", cols)
	cols[0].Name = "MUTATED"
	require.Equal(t, sampleColumns(), mustCols(t, s, "public", "users"))

	fks := sampleFKs()
	s.SetForeignKeys("public", "orders", fks)
	fks[0].Columns[0] = "MUTATED"
	require.Equal(t, sampleFKs(), mustFKs(t, s, "public", "orders"))

	entries := sampleTableEntries()
	s.SetTables("public", entries)
	entries[0].Name = "MUTATED"
	require.Equal(t, []string{"users", "orders"}, s.TableNames("public"))
}

func TestSchemaMetadataStore_InvalidateTable(t *testing.T) {
	s := NewSchemaMetadataStore()
	s.SetTables("public", sampleTableEntries())
	s.SetColumns("public", "users", sampleColumns())
	s.SetForeignKeys("public", "users", sampleFKs())
	s.SetColumns("public", "orders", sampleColumns())

	s.InvalidateTable("public", "users")

	// Invalidated table's lazy entries are gone.
	_, ok := s.Columns("public", "users")
	require.False(t, ok)
	_, ok = s.ForeignKeys("public", "users")
	require.False(t, ok)

	// Other table's lazy entry is untouched.
	_, ok = s.Columns("public", "orders")
	require.True(t, ok)

	// Eager table-name list is untouched.
	require.Equal(t, []string{"users", "orders"}, s.TableNames("public"))
}

func TestSchemaMetadataStore_Reset(t *testing.T) {
	s := NewSchemaMetadataStore()
	s.SetTables("public", []TableEntry{{Name: "users", Kind: "table"}})
	s.SetFunctionNames([]string{"now"})
	s.SetColumns("public", "users", sampleColumns())
	s.SetForeignKeys("public", "orders", sampleFKs())

	s.Reset()

	require.Nil(t, s.TableNames("public"))
	require.Nil(t, s.FunctionNames())
	_, ok := s.Columns("public", "users")
	require.False(t, ok)
	_, ok = s.ForeignKeys("public", "orders")
	require.False(t, ok)
}

// Scenario: concurrent reads of one table while writing a different table must
// be race-clean and return consistent copies. Plus a broad mix of
// SetX/GetX/InvalidateTable/Reset under -race.
func TestSchemaMetadataStore_ConcurrentAccess(t *testing.T) {
	s := NewSchemaMetadataStore()
	s.SetColumns("public", "users", sampleColumns())

	const workers = 8
	const iters = 500

	var wg sync.WaitGroup

	// Readers of "users" assert consistency on every read.
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range iters {
				cols, ok := s.Columns("public", "users")
				if ok {
					require.Equal(t, "id", cols[0].Name)
				}
			}
		}()
	}

	// Writers churn a different table + eager tiers.
	for w := range workers {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for range iters {
				s.SetColumns("public", "orders", sampleColumns())
				s.SetForeignKeys("public", "orders", sampleFKs())
				s.SetTables("public", sampleTableEntries())
				s.SetFunctionNames([]string{"now"})
				_, _ = s.ForeignKeys("public", "orders")
				_ = s.TableNames("public")
				_ = s.FunctionNames()
				s.InvalidateTable("public", "orders")
			}
		}(w)
	}

	// One goroutine periodically resets the whole store.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range iters {
			s.Reset()
			s.SetColumns("public", "users", sampleColumns())
		}
	}()

	wg.Wait()
}

func mustCols(t *testing.T, s *SchemaMetadataStore, schema, table string) []models.Column {
	t.Helper()
	cols, ok := s.Columns(schema, table)
	require.True(t, ok)
	return cols
}

func mustFKs(t *testing.T, s *SchemaMetadataStore, schema, table string) []models.ForeignKey {
	t.Helper()
	fks, ok := s.ForeignKeys(schema, table)
	require.True(t, ok)
	return fks
}
