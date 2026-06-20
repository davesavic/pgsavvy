package editor

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/models"
)

// fakeMeta is a synchronous, in-memory SchemaMetadata fake. Tests pre-populate
// the table/column/function tiers; SchemaSource reads them with no driver call.
// It records nothing — the reactive-warm assertions live on fakeWarmer.
type fakeMeta struct {
	mu sync.Mutex

	tableNames map[string][]string            // schema -> names
	tableKinds map[string]string              // schema\x00name -> kind
	columns    map[string][]models.Column     // schema\x00table -> cols
	foreignKey map[string][]models.ForeignKey // schema\x00table -> fks
	functions  []string
	hasFns     bool
}

func newFakeMeta() *fakeMeta {
	return &fakeMeta{
		tableNames: map[string][]string{},
		tableKinds: map[string]string{},
		columns:    map[string][]models.Column{},
		foreignKey: map[string][]models.ForeignKey{},
	}
}

func metaKey(schema, table string) string { return schema + "\x00" + table }

func (m *fakeMeta) setTables(schema string, names ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tableNames[schema] = names
}

func (m *fakeMeta) setTableKind(schema, name, kind string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tableKinds[metaKey(schema, name)] = kind
}

func (m *fakeMeta) setColumns(schema, table string, cols ...models.Column) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.columns[metaKey(schema, table)] = cols
}

func (m *fakeMeta) setForeignKeys(schema, table string, fks ...models.ForeignKey) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.foreignKey[metaKey(schema, table)] = fks
}

func (m *fakeMeta) setFunctions(names ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.functions = names
	m.hasFns = true
}

func (m *fakeMeta) TableNames(schema string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names, ok := m.tableNames[schema]
	if !ok {
		return nil
	}
	out := make([]string, len(names))
	copy(out, names)
	return out
}

func (m *fakeMeta) TableKind(schema, name string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tableKinds[metaKey(schema, name)]
}

func (m *fakeMeta) Columns(schema, table string) ([]models.Column, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cols, ok := m.columns[metaKey(schema, table)]
	if !ok {
		return nil, false
	}
	out := make([]models.Column, len(cols))
	copy(out, cols)
	return out, true
}

func (m *fakeMeta) ForeignKeys(schema, table string) ([]models.ForeignKey, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fks, ok := m.foreignKey[metaKey(schema, table)]
	if !ok {
		return nil, false
	}
	out := make([]models.ForeignKey, len(fks))
	copy(out, fks)
	return out, true
}

func (m *fakeMeta) FunctionNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.hasFns {
		return nil
	}
	out := make([]string, len(m.functions))
	copy(out, m.functions)
	return out
}

// fakeWarmer records every WarmTable(schema,table) call so tests assert the
// reactive-warm contract: exactly one warm per referenced-but-unloaded table,
// non-blocking. It does NOT mutate the store — a real warm publishes async; the
// re-trigger is exercised in the controller test.
type fakeWarmer struct {
	mu    sync.Mutex
	calls [][2]string
}

func (w *fakeWarmer) WarmTable(schema, table string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, [2]string{schema, table})
}

func (w *fakeWarmer) warmed() [][2]string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([][2]string, len(w.calls))
	copy(out, w.calls)
	return out
}

func bufWithCursor(line string) (*Buffer, Position) {
	b := bufFromLines(line)
	return b, Position{Line: 0, Col: len([]rune(line))}
}

func schemaProv(s string) SchemaProvider { return func() string { return s } }

func suggestLine(src *SchemaSource, line string) []Suggestion {
	b, p := bufWithCursor(line)
	return src.Suggest(context.Background(), b, p)
}

func texts(sugs []Suggestion) []string {
	out := make([]string, 0, len(sugs))
	for _, s := range sugs {
		out = append(out, s.Text)
	}
	return out
}

func equalStrings(a, b []string) bool {
	return slices.Equal(a, b)
}

func TestSchemaSource_Identity(t *testing.T) {
	src := NewSchemaSource(nil, nil, nil)
	if src.Name() != SchemaSourceName {
		t.Errorf("Name() = %q; want %q", src.Name(), SchemaSourceName)
	}
	if src.Priority() != SchemaSourcePriority {
		t.Errorf("Priority() = %d; want %d", src.Priority(), SchemaSourcePriority)
	}
}

func TestSchemaSource_NilMeta_Empty(t *testing.T) {
	src := NewSchemaSource(nil, &fakeWarmer{}, schemaProv("public"))
	got := suggestLine(src, "SELECT * FROM ")
	if got == nil || len(got) != 0 {
		t.Fatalf("Suggest with nil meta = %+v; want empty non-nil", got)
	}
}

func TestSchemaSource_NilBuffer_Empty(t *testing.T) {
	src := NewSchemaSource(newFakeMeta(), &fakeWarmer{}, schemaProv("public"))
	got := src.Suggest(context.Background(), nil, Position{})
	if got == nil || len(got) != 0 {
		t.Fatalf("Suggest(nil buffer) = %+v; want empty", got)
	}
}

func TestSchemaSource_EmptySchema_Empty(t *testing.T) {
	m := newFakeMeta()
	m.setTables("public", "users")
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv(""))
	got := suggestLine(src, "SELECT * FROM ")
	if len(got) != 0 {
		t.Fatalf("len(got) = %d; want 0 (empty schema)", len(got))
	}
}

func TestSchemaSource_TableContexts(t *testing.T) {
	tests := []struct {
		name string
		line string
		want bool // true = should suggest tables
	}{
		{"trailing FROM", "SELECT * FROM ", true},
		{"trailing lowercase from", "select * from ", true},
		{"trailing JOIN", "SELECT * FROM a JOIN ", true},
		{"trailing INNER JOIN", "SELECT * FROM a INNER JOIN ", true},
		{"trailing LEFT JOIN", "SELECT * FROM a LEFT JOIN ", true},
		{"trailing RIGHT JOIN", "SELECT * FROM a RIGHT JOIN ", true},
		{"trailing CROSS JOIN", "SELECT * FROM a CROSS JOIN ", true},
		{"trailing UPDATE", "UPDATE ", true},
		{"trailing INTO", "INSERT INTO ", true},

		{"FROM no trailing space", "SELECT * FROM", false},
		{"INNER no space then JOIN no space", "SELECT * FROM a INNER JOIN", false},
		{"XFROM (word boundary)", "SELECT * XFROM ", false},
		{"FROMX (word boundary)", "SELECT * FROMX ", false},
		{"trailing FROM with tab counts as whitespace", "SELECT * FROM\t", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newFakeMeta()
			m.setTables("public", "users", "orders")
			src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
			got := suggestLine(src, tc.line)
			if tc.want {
				if len(got) != 2 {
					t.Fatalf("len(got) = %d; want 2 tables for line %q", len(got), tc.line)
				}
				if got[0].Text != "users" || got[0].Display != "users" {
					t.Errorf("got[0] = %+v; want {Text:users, Display:users}", got[0])
				}
				if got[0].Source != SchemaSourceName {
					t.Errorf("got[0].Source = %q; want %q", got[0].Source, SchemaSourceName)
				}
				return
			}
			if len(got) != 0 {
				t.Fatalf("len(got) = %d; want 0 for line %q (no match expected)", len(got), tc.line)
			}
		})
	}
}

// TestSchemaSource_TablesUnloaded_Empty: a FROM context whose schema has no
// eager table names yet returns empty without a driver call (table names are
// eager-loaded, not lazily warmed).
func TestSchemaSource_TablesUnloaded_Empty(t *testing.T) {
	m := newFakeMeta() // no tables set for public
	w := &fakeWarmer{}
	src := NewSchemaSource(m, w, schemaProv("public"))
	if got := suggestLine(src, "SELECT * FROM "); len(got) != 0 {
		t.Fatalf("got %v; want empty (table names not eager-loaded)", texts(got))
	}
	if len(w.warmed()) != 0 {
		t.Errorf("WarmTable called %v; want none for a FROM/table context", w.warmed())
	}
}

// TestSchemaSource_TableKind: a FROM-context table suggestion carries Kind=
// KindView for a view / materialized view and Kind=KindTable for a plain or
// partitioned table, read from the eager snapshot's per-name kind.
// An eager name with no recorded kind ("" — e.g. older snapshot)
// falls back to KindTable.
func TestSchemaSource_TableKind(t *testing.T) {
	m := newFakeMeta()
	m.setTables("public", "users", "active_users", "user_stats", "events", "legacy")
	m.setTableKind("public", "users", "table")
	m.setTableKind("public", "active_users", "view")
	m.setTableKind("public", "user_stats", "materialized_view")
	m.setTableKind("public", "events", "partitioned_table")
	// "legacy" intentionally left with no kind -> "" -> KindTable fallback.

	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	got := suggestLine(src, "SELECT * FROM ")

	want := map[string]SuggestionKind{
		"users":        KindTable,
		"active_users": KindView,
		"user_stats":   KindView,
		"events":       KindTable,
		"legacy":       KindTable,
	}
	if len(got) != len(want) {
		t.Fatalf("len(got) = %d; want %d", len(got), len(want))
	}
	for _, sg := range got {
		if wk, ok := want[sg.Text]; !ok {
			t.Errorf("unexpected suggestion %q", sg.Text)
		} else if sg.Kind != wk {
			t.Errorf("%q Kind = %q; want %q", sg.Text, sg.Kind, wk)
		}
	}
}

func TestSchemaSource_ColumnContext_Loaded(t *testing.T) {
	cols := []models.Column{
		{Name: "id", DataType: "integer"},
		{Name: "email", DataType: "text"},
	}
	tests := []struct {
		name      string
		line      string
		wantTable string
	}{
		{"users.", "SELECT users.", "users"},
		{"WHERE users.", "SELECT * FROM users WHERE users.", "users"},
		{"mixed-case Users.", "SELECT Users.", "Users"},
		{"schema-qualified public.users.", "SELECT public.users.", "users"},
		{"quoted MyTable.", `SELECT "MyTable".`, "MyTable"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newFakeMeta()
			m.setColumns("public", tc.wantTable, cols...)
			src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
			got := suggestLine(src, tc.line)
			if len(got) != 2 {
				t.Fatalf("len(got) = %d; want 2 cols for line %q", len(got), tc.line)
			}
			if got[0].Text != "id" {
				t.Errorf("got[0].Text = %q; want id", got[0].Text)
			}
			if got[0].Display != "id · integer" {
				t.Errorf("got[0].Display = %q; want %q", got[0].Display, "id · integer")
			}
			if got[1].Display != "email · text" {
				t.Errorf("got[1].Display = %q; want %q", got[1].Display, "email · text")
			}
		})
	}
}

// TestSchemaSource_ColumnContext_UnloadedWarmsOnce is the headline reactive
// contract: a referenced table whose columns are unloaded triggers exactly ONE
// non-blocking WarmTable and Suggest returns immediately with empty.
func TestSchemaSource_ColumnContext_UnloadedWarmsOnce(t *testing.T) {
	m := newFakeMeta() // users not loaded
	w := &fakeWarmer{}
	src := NewSchemaSource(m, w, schemaProv("public"))

	got := suggestLine(src, "SELECT users.")
	if len(got) != 0 {
		t.Fatalf("got %v; want empty (columns unloaded)", texts(got))
	}
	warmed := w.warmed()
	if len(warmed) != 1 || warmed[0] != [2]string{"public", "users"} {
		t.Fatalf("warmed = %v; want exactly one WarmTable(public,users)", warmed)
	}
}

// TestSchemaSource_SchemaQualifiedWarmKey: when the engine resolves a
// schema-qualified in-scope table (FROM app.orders o ... o.), the warm key uses
// that table's OWN schema (app), not the active-schema default (public). This
// is the store-key behavior the qualifier's Schema field drives.
func TestSchemaSource_SchemaQualifiedWarmKey(t *testing.T) {
	m := newFakeMeta()
	w := &fakeWarmer{}
	src := NewSchemaSource(m, w, schemaProv("public"))
	_ = suggestLine(src, "SELECT * FROM app.orders o WHERE o.")
	warmed := w.warmed()
	if len(warmed) != 1 || warmed[0] != [2]string{"app", "orders"} {
		t.Fatalf("warmed = %v; want one WarmTable(app,orders)", warmed)
	}
}

// TestSchemaSource_BareDotQualifierUsesActiveSchema: a bare `public.users.`
// dot-qualifier (no FROM) does NOT carry a resolved schema from the engine, so
// the warm/read key falls back to the active-schema default — matching the
// pre-store behavior (ListColumns took the active schema verbatim).
func TestSchemaSource_BareDotQualifierUsesActiveSchema(t *testing.T) {
	m := newFakeMeta()
	w := &fakeWarmer{}
	src := NewSchemaSource(m, w, schemaProv("public"))
	_ = suggestLine(src, "SELECT public.users.")
	warmed := w.warmed()
	if len(warmed) != 1 || warmed[0] != [2]string{"public", "users"} {
		t.Fatalf("warmed = %v; want one WarmTable(public,users)", warmed)
	}
}

func TestSchemaSource_ColumnDisplayFallsBackWithoutType(t *testing.T) {
	m := newFakeMeta()
	m.setColumns("public", "users", models.Column{Name: "id"})
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	got := suggestLine(src, "SELECT users.")
	if len(got) != 1 {
		t.Fatalf("len(got) = %d; want 1", len(got))
	}
	if got[0].Display != "id" {
		t.Errorf("Display = %q; want %q (no type → no separator)", got[0].Display, "id")
	}
}

func TestSchemaSource_InsideStringLiteral_Empty(t *testing.T) {
	m := newFakeMeta()
	m.setTables("public", "users")
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	tests := []string{
		"SELECT 'SELECT * FROM ",   // unclosed single-quoted
		"SELECT 'a' || 'and FROM ", // second open string
	}
	for _, line := range tests {
		t.Run(line, func(t *testing.T) {
			got := suggestLine(src, line)
			if len(got) != 0 {
				t.Fatalf("len(got) = %d; want 0 for %q", len(got), line)
			}
		})
	}
}

func TestSchemaSource_InsideLineComment_Empty(t *testing.T) {
	m := newFakeMeta()
	m.setTables("public", "users")
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	if got := suggestLine(src, "SELECT 1 -- FROM "); len(got) != 0 {
		t.Fatalf("len(got) = %d; want 0 (inside -- comment)", len(got))
	}
}

func TestSchemaSource_InsideBlockComment_Empty(t *testing.T) {
	m := newFakeMeta()
	m.setTables("public", "users")
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	if got := suggestLine(src, "SELECT 1 /* FROM "); len(got) != 0 {
		t.Fatalf("len(got) = %d; want 0 (inside /* block)", len(got))
	}
}

func TestSchemaSource_ClosedBlockComment_StillTriggers(t *testing.T) {
	m := newFakeMeta()
	m.setTables("public", "users")
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	if got := suggestLine(src, "SELECT 1 /* nope */ FROM "); len(got) != 1 {
		t.Fatalf("len(got) = %d; want 1 (FROM after closed /* */)", len(got))
	}
}

func TestSchemaSource_EscapedQuotesInString(t *testing.T) {
	m := newFakeMeta()
	m.setTables("public", "users")
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	if got := suggestLine(src, "SELECT 'it''s FROM "); len(got) != 0 {
		t.Fatalf("len(got) = %d; want 0 (FROM inside escaped string)", len(got))
	}
	if got := suggestLine(src, "SELECT 'it''s ok' FROM "); len(got) != 1 {
		t.Fatalf("len(got) = %d; want 1 (FROM after closed string)", len(got))
	}
}

func TestSchemaSource_ZeroTables_Empty(t *testing.T) {
	m := newFakeMeta()
	m.setTables("public") // explicit empty
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	got := suggestLine(src, "SELECT * FROM ")
	if got == nil || len(got) != 0 {
		t.Fatalf("len(got) = %d; want empty for zero tables", len(got))
	}
}

func TestSchemaSource_MissingTable_WarmsThenEmpty(t *testing.T) {
	m := newFakeMeta() // typo'd table unloaded
	w := &fakeWarmer{}
	src := NewSchemaSource(m, w, schemaProv("public"))
	got := suggestLine(src, "SELECT usres.")
	if len(got) != 0 {
		t.Fatalf("len(got) = %d; want 0 for unloaded table", len(got))
	}
	warmed := w.warmed()
	if len(warmed) != 1 || warmed[0][1] != "usres" {
		t.Errorf("warmed = %v; want WarmTable for verbatim 'usres'", warmed)
	}
}

func TestSchemaSource_PreservesIdentifierCase(t *testing.T) {
	m := newFakeMeta()
	w := &fakeWarmer{}
	src := NewSchemaSource(m, w, schemaProv("public"))
	_ = suggestLine(src, "SELECT MyTable.")
	warmed := w.warmed()
	if len(warmed) != 1 || warmed[0][1] != "MyTable" {
		t.Errorf("warmed = %v; want preserved 'MyTable'", warmed)
	}
}

func TestSchemaSource_CursorMidLine_UsesLineUpToCursor(t *testing.T) {
	m := newFakeMeta()
	m.setTables("public", "users")
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	full := "SELECT * FROM ; -- trailing"
	b := bufFromLines(full)
	p := Position{Line: 0, Col: len([]rune("SELECT * FROM "))}
	got := src.Suggest(context.Background(), b, p)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d; want 1 (line-up-to-cursor ends at FROM ' ')", len(got))
	}
}

// TestSchemaSource_NoiseSuppression: a trailing FROM inside a string / comment /
// dollar-quote suppresses suggestions; a FROM after a CLOSED noise construct
// still triggers.
func TestSchemaSource_NoiseSuppression(t *testing.T) {
	tests := []struct {
		line      string
		wantEmpty bool
	}{
		{"SELECT * FROM ", false},
		{"SELECT 'abc' FROM ", false},
		{"SELECT 'a''b' FROM ", false},
		{"SELECT $$x$$ FROM ", false},
		{"SELECT $tag$x$tag$ FROM ", false},
		{"SELECT /* x */ FROM ", false},
		{"SELECT 'abc FROM ", true},
		{"SELECT 1 -- FROM ", true},
		{"SELECT 1 /* FROM ", true},
	}
	for _, tc := range tests {
		t.Run(tc.line, func(t *testing.T) {
			m := newFakeMeta()
			m.setTables("public", "users")
			src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
			got := suggestLine(src, tc.line)
			if tc.wantEmpty && len(got) != 0 {
				t.Fatalf("Suggest(%q) = %v; want empty (noise)", tc.line, texts(got))
			}
			if !tc.wantEmpty && len(got) == 0 {
				t.Fatalf("Suggest(%q) = empty; want table suggestions", tc.line)
			}
		})
	}
}

// TestSchemaSource_JoinOnContext: the JOIN ... ON / USING position offers the
// in-scope tables plus their (loaded) columns.
func TestSchemaSource_JoinOnContext(t *testing.T) {
	m := newFakeMeta()
	m.setTables("public", "posts", "posts_summary")
	m.setColumns("public", "posts", models.Column{Name: "id"}, models.Column{Name: "post_id"})
	m.setColumns("public", "posts_summary", models.Column{Name: "id"}, models.Column{Name: "post_id"})
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))

	got := texts(suggestLine(src, "select * from posts join posts_summary on "))
	for _, want := range []string{"posts", "posts_summary", "id", "post_id"} {
		if !slices.Contains(got, want) {
			t.Errorf("ON-context (empty partial) missing %q; got %v", want, got)
		}
	}

	got2 := texts(suggestLine(src, "select * from posts join posts_summary on posts"))
	if !slices.Contains(got2, "posts") || !slices.Contains(got2, "posts_summary") {
		t.Errorf("ON-context prefix `posts` should offer posts/posts_summary; got %v", got2)
	}

	if len(suggestLine(src, "select * from posts join posts_summary using ")) == 0 {
		t.Error("USING context returned no suggestions")
	}
}

func TestSchemaSource_TablePrefixFilter(t *testing.T) {
	tests := []struct {
		name string
		line string
		want []string
	}{
		{"empty partial returns all", "SELECT * FROM ", []string{"users", "usage", "orders"}},
		{"partial us narrows", "SELECT * FROM us", []string{"users", "usage"}},
		{"case-insensitive partial US", "SELECT * FROM US", []string{"users", "usage"}},
		{"no-match partial zz empty", "SELECT * FROM zz", nil},
		{"full name then space is an alias slot, offers nothing", "SELECT * FROM users ", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newFakeMeta()
			m.setTables("public", "users", "usage", "orders")
			src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
			got := texts(suggestLine(src, tc.line))
			if len(got) != len(tc.want) {
				t.Fatalf("Suggest(%q) = %v; want %v", tc.line, got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("Suggest(%q) = %v; want %v", tc.line, got, tc.want)
				}
			}
		})
	}
}

func TestSchemaSource_ColumnPrefixFilter(t *testing.T) {
	cols := []models.Column{
		{Name: "created_at", DataType: "timestamp"},
		{Name: "credit", DataType: "int"},
		{Name: "id", DataType: "int"},
	}
	tests := []struct {
		name string
		line string
		want []string
	}{
		{"empty partial returns all", "SELECT users.", []string{"created_at", "credit", "id"}},
		{"partial cr narrows", "SELECT users.cr", []string{"created_at", "credit"}},
		{"case-insensitive CR", "SELECT users.CR", []string{"created_at", "credit"}},
		{"no-match partial zz empty", "SELECT users.zz", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newFakeMeta()
			m.setColumns("public", "users", cols...)
			src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
			got := texts(suggestLine(src, tc.line))
			if len(got) != len(tc.want) {
				t.Fatalf("Suggest(%q) = %v; want %v", tc.line, got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("Suggest(%q) = %v; want %v", tc.line, got, tc.want)
				}
			}
		})
	}
}

// TestSchemaSource_WarmOncePerUnloadedTable: re-issuing the same unloaded
// column lookup warms each time the source is asked (the warmer itself
// deduplicates in-flight warms; the source's job is only to ASK once per
// Suggest). Two distinct unloaded tables produce two distinct warm keys.
func TestSchemaSource_WarmKeysPerTable(t *testing.T) {
	m := newFakeMeta()
	w := &fakeWarmer{}
	src := NewSchemaSource(m, w, schemaProv("public"))
	_ = suggestLine(src, "SELECT users.")
	_ = suggestLine(src, "SELECT orders.")
	warmed := w.warmed()
	if len(warmed) != 2 {
		t.Fatalf("warmed = %v; want 2 (one per table)", warmed)
	}
	if warmed[0] != [2]string{"public", "users"} || warmed[1] != [2]string{"public", "orders"} {
		t.Fatalf("warmed = %v; want [(public,users) (public,orders)]", warmed)
	}
}

// TestSchemaSource_NilWarmer_NoPanic: a column miss with no warmer wired must
// not panic and returns empty.
func TestSchemaSource_NilWarmer_NoPanic(t *testing.T) {
	m := newFakeMeta()
	src := NewSchemaSource(m, nil, schemaProv("public"))
	if got := suggestLine(src, "SELECT users."); len(got) != 0 {
		t.Fatalf("got %v; want empty (unloaded, no warmer)", texts(got))
	}
}

func TestSchemaSource_UnqualifiedColumns_AfterWhere(t *testing.T) {
	m := newFakeMeta()
	m.setColumns("public", "posts", models.Column{Name: "id", DataType: "int"}, models.Column{Name: "title", DataType: "text"})
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	got := texts(suggestLine(src, "SELECT * FROM posts WHERE "))
	if !equalStrings(got, []string{"id", "title"}) {
		t.Fatalf("got %v; want [id title]", got)
	}
}

func TestSchemaSource_UnqualifiedColumns_AfterSelectWithLaterFrom(t *testing.T) {
	m := newFakeMeta()
	m.setColumns("public", "users", models.Column{Name: "id"}, models.Column{Name: "name"})
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	full := "SELECT  FROM users"
	b := bufFromLines(full)
	p := Position{Line: 0, Col: len([]rune("SELECT "))}
	got := texts(src.Suggest(context.Background(), b, p))
	if !equalStrings(got, []string{"id", "name"}) {
		t.Fatalf("got %v; want [id name]", got)
	}
}

func TestSchemaSource_UnqualifiedColumns_OperatorAndKeywordContexts(t *testing.T) {
	for _, line := range []string{
		"SELECT * FROM posts WHERE ",
		"SELECT * FROM posts WHERE id = ",
		"SELECT * FROM posts WHERE id = 1 AND ",
		"SELECT * FROM posts WHERE id = 1 OR ",
	} {
		m := newFakeMeta()
		m.setColumns("public", "posts", models.Column{Name: "id"})
		src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
		got := texts(suggestLine(src, line))
		if len(got) != 1 || got[0] != "id" {
			t.Fatalf("line %q -> %v; want [id]", line, got)
		}
	}
}

func TestSchemaSource_UnqualifiedColumns_PrefixFilter(t *testing.T) {
	m := newFakeMeta()
	m.setColumns("public", "users", models.Column{Name: "id"}, models.Column{Name: "email"}, models.Column{Name: "events"})
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	got := texts(suggestLine(src, "SELECT * FROM users WHERE e"))
	if !equalStrings(got, []string{"email", "events"}) {
		t.Fatalf("got %v; want [email events]", got)
	}
}

func TestSchemaSource_UnqualifiedColumns_MultiTableUnionDedupe(t *testing.T) {
	m := newFakeMeta()
	m.setColumns("public", "users", models.Column{Name: "id"}, models.Column{Name: "email"})
	m.setColumns("public", "orders", models.Column{Name: "id"}, models.Column{Name: "total"})
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	got := texts(suggestLine(src, "SELECT * FROM users u JOIN orders o ON u.id = o.user_id WHERE "))
	if !equalStrings(got, []string{"id", "email", "total"}) {
		t.Fatalf("got %v; want [id email total] (union, deduped)", got)
	}
}

func TestSchemaSource_UnqualifiedColumns_NoTableInScope_Empty(t *testing.T) {
	m := newFakeMeta()
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	if got := suggestLine(src, "SELECT "); len(got) != 0 {
		t.Fatalf("got %v; want empty (no FROM table in scope)", got)
	}
}

func TestSchemaSource_ColumnContext_InsideStringSuppressed(t *testing.T) {
	m := newFakeMeta()
	m.setColumns("public", "users", models.Column{Name: "id"})
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	if got := suggestLine(src, "SELECT * FROM users WHERE 'abc"); len(got) != 0 {
		t.Fatalf("got %v; want empty (cursor inside string literal)", got)
	}
}

// TestSchemaSource_AliasDotResolvesTable: with "FROM users u JOIN orders o ON o."
// the trailing `o.` resolves to orders (alias o), so only orders' columns show.
func TestSchemaSource_AliasDotResolvesTable(t *testing.T) {
	m := newFakeMeta()
	m.setColumns("public", "users", models.Column{Name: "user_id"})
	m.setColumns("public", "orders", models.Column{Name: "order_id"})
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	got := texts(suggestLine(src, "SELECT * FROM users u JOIN orders o ON o."))
	if !equalStrings(got, []string{"order_id"}) {
		t.Fatalf("got %v; want [order_id] (alias o -> orders, not users)", got)
	}
}

func TestSchemaSource_HalfTypedSelect_NoPanic(t *testing.T) {
	m := newFakeMeta()
	m.setColumns("public", "users", models.Column{Name: "name"}, models.Column{Name: "id"})
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	full := "SELECT id, na FROM users"
	b := bufFromLines(full)
	p := Position{Line: 0, Col: len([]rune("SELECT id, na"))}
	got := texts(src.Suggest(context.Background(), b, p))
	if !slices.Contains(got, "name") {
		t.Fatalf("got %v; want a column starting with na (e.g. name)", got)
	}
}

func TestSchemaSource_MultiLineNoise_NoSuggestions(t *testing.T) {
	m := newFakeMeta()
	m.setTables("public", "users")
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))

	b := bufFromLines("SELECT 'open", "FROM ")
	p := Position{Line: 1, Col: len([]rune("FROM "))}
	if got := src.Suggest(context.Background(), b, p); len(got) != 0 {
		t.Fatalf("multi-line string: got %v; want empty", texts(got))
	}

	b2 := bufFromLines("SELECT 1 /* open", "FROM ")
	p2 := Position{Line: 1, Col: len([]rune("FROM "))}
	if got := src.Suggest(context.Background(), b2, p2); len(got) != 0 {
		t.Fatalf("multi-line comment: got %v; want empty", texts(got))
	}
}

func TestSchemaSource_MultiStatementScope(t *testing.T) {
	m := newFakeMeta()
	m.setColumns("public", "users", models.Column{Name: "user_col"})
	m.setColumns("public", "orders", models.Column{Name: "order_col"})
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	full := "SELECT * FROM users; SELECT * FROM orders WHERE "
	b := bufFromLines(full)
	p := Position{Line: 0, Col: len([]rune(full))}
	got := texts(src.Suggest(context.Background(), b, p))
	if !equalStrings(got, []string{"order_col"}) {
		t.Fatalf("got %v; want [order_col] (only cursor's statement scope)", got)
	}
}

func TestSchemaSource_PartialInvalidSQL_NoPanic(t *testing.T) {
	m := newFakeMeta()
	m.setTables("public", "users")
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	for _, line := range []string{
		"SELEC",
		"((((",
		"FROM FROM FROM",
		"SELECT * FROM (",
		")(.,;",
	} {
		_ = suggestLine(src, line)
	}
}

// TestSchemaSource_FuzzyTableSubsequence: typing "usr" surfaces "user_sessions"
// via subsequence match (u-s-r), which prefix matching would have missed.
func TestSchemaSource_FuzzyTableSubsequence(t *testing.T) {
	m := newFakeMeta()
	m.setTables("public", "user_sessions", "orders")
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	got := texts(suggestLine(src, "SELECT * FROM usr"))
	if !slices.Contains(got, "user_sessions") {
		t.Fatalf("got %v; want user_sessions surfaced via subsequence 'usr'", got)
	}
	if slices.Contains(got, "orders") {
		t.Fatalf("got %v; orders has no 'usr' subsequence and must be excluded", got)
	}
}

// TestSchemaSource_FuzzyColumnSubsequence: typing "oeml" surfaces "order_email"
// via subsequence match (o-e-m-l).
func TestSchemaSource_FuzzyColumnSubsequence(t *testing.T) {
	m := newFakeMeta()
	m.setColumns("public", "orders", models.Column{Name: "order_email"}, models.Column{Name: "id"})
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	got := texts(suggestLine(src, "SELECT orders.oeml"))
	if !slices.Contains(got, "order_email") {
		t.Fatalf("got %v; want order_email surfaced via subsequence 'oeml'", got)
	}
}

// TestSchemaSource_CompositeScoreAndMatches: each emitted Suggestion carries a
// composite Score (matchQuality + SchemaSourceBias, never the old literal 3) and
// Matches populated from Match's rune positions.
func TestSchemaSource_CompositeScoreAndMatches(t *testing.T) {
	m := newFakeMeta()
	m.setTables("public", "users")
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	got := suggestLine(src, "SELECT * FROM us")
	if len(got) != 1 || got[0].Text != "users" {
		t.Fatalf("got %v; want one suggestion 'users'", texts(got))
	}
	ok, quality, positions := Match("us", "users")
	if !ok {
		t.Fatal("precondition: Match(us,users) should be ok")
	}
	wantScore := quality + SchemaSourceBias
	if got[0].Score != wantScore {
		t.Errorf("Score = %d; want %d (matchQuality %d + SchemaSourceBias %d)", got[0].Score, wantScore, quality, SchemaSourceBias)
	}
	if got[0].Score == 3 {
		t.Errorf("Score = 3; must NOT be the old literal SchemaSourceScore")
	}
	if !slices.Equal(got[0].Matches, positions) {
		t.Errorf("Matches = %v; want %v (rune offsets into Text from Match)", got[0].Matches, positions)
	}
}

// TestSchemaSource_EmptyPrefixFullListNilMatches: an empty identifier prefix in
// a table context returns the full in-scope list, each with Score =
// SchemaSourceBias (matchQuality 0) and nil Matches (Match("",x) contract).
func TestSchemaSource_EmptyPrefixFullListNilMatches(t *testing.T) {
	m := newFakeMeta()
	m.setTables("public", "users", "orders")
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	got := suggestLine(src, "SELECT * FROM ")
	if len(got) != 2 {
		t.Fatalf("len(got) = %d; want 2 (full list on empty prefix)", len(got))
	}
	for _, sg := range got {
		if sg.Score != SchemaSourceBias {
			t.Errorf("%q Score = %d; want SchemaSourceBias %d (matchQuality 0)", sg.Text, sg.Score, SchemaSourceBias)
		}
		if sg.Matches != nil {
			t.Errorf("%q Matches = %v; want nil on empty prefix", sg.Text, sg.Matches)
		}
	}
}

// TestSchemaSource_OneCharOverlapExcluded: a multi-char prefix sharing only a
// single scattered char with a candidate is dropped by Match's quality floor
// (ok=false), so no extra filtering is needed in the source.
func TestSchemaSource_OneCharOverlapExcluded(t *testing.T) {
	m := newFakeMeta()
	// "xq" shares at most one char with "orders" and is not a subsequence of it
	// either; assert the quality-floor / non-subsequence path excludes it.
	m.setColumns("public", "orders", models.Column{Name: "shipped_at"})
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	if got := suggestLine(src, "SELECT orders.xq"); len(got) != 0 {
		t.Fatalf("got %v; want empty (1-char-overlap junk excluded via Match ok=false)", texts(got))
	}
}

// TestSchemaSource_NonASCIIColumnRuneOffsets: a non-ASCII column name yields
// Matches as RUNE offsets (not byte offsets) into Text.
func TestSchemaSource_NonASCIIColumnRuneOffsets(t *testing.T) {
	m := newFakeMeta()
	m.setColumns("public", "orders", models.Column{Name: "naïve_total"})
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	got := suggestLine(src, "SELECT orders.nt")
	if len(got) != 1 || got[0].Text != "naïve_total" {
		t.Fatalf("got %v; want [naïve_total]", texts(got))
	}
	ok, _, positions := Match("nt", "naïve_total")
	if !ok {
		t.Fatal("precondition: Match(nt, naïve_total) should be ok")
	}
	if !slices.Equal(got[0].Matches, positions) {
		t.Errorf("Matches = %v; want %v (rune offsets, not byte)", got[0].Matches, positions)
	}
	// 't' in "total" is rune index 6 (n=0,a=1,ï=2,v=3,e=4,_=5,t=6); a byte-based
	// matcher would have produced 7 because ï is two bytes. Guard against regression.
	if len(positions) == 2 && positions[1] != 6 {
		t.Errorf("second match rune offset = %d; want 6 (rune-indexed past ï)", positions[1])
	}
}

// TestEngine_SchemaTablesOutrankKeywords proves a schema table outranks a
// keyword AT COMPARABLE MATCH QUALITY through the composite Score
// (matchQuality + sourceBias, SchemaSourceBias 80 > KeywordSourceBias 40).
//
// Reconciled from the OLD invariant — "schema constant
// Score=3 always beats keyword" (a walkover that held even on an empty-prefix
// match-all where every source scored identically) — to "schema outranks
// keyword when both compete at the SAME match quality". The table name is
// chosen so the typed prefix "se" fuzzily matches BOTH a real keyword (SELECT,
// SET) and the table "session_data" at identical quality (q=78 each), so the
// schema entry only reaches got[0] by winning the bias tie-break, and we assert
// a keyword ALSO survived and ranked lower — a genuine competition, not a
// constant-wins walkover.
func TestEngine_SchemaTablesOutrankKeywords(t *testing.T) {
	m := newFakeMeta()
	m.setTables("app", "session_data")
	schema := NewSchemaSource(m, &fakeWarmer{}, schemaProv("app"))
	eng := NewEngine([]Source{KeywordsSource{PriorityVal: 20}, schema})

	// Prefix "se" at a FROM context: schema offers session_data, keywords offer
	// SELECT/SET — all at the same match quality, competing on bias alone.
	b, p := bufWithCursor("SELECT * FROM se")
	got := eng.Trigger(context.Background(), b, p)
	if len(got) == 0 {
		t.Fatal("Trigger returned no suggestions")
	}

	// Precondition: the prefix must genuinely match both the table and a keyword
	// at equal quality, otherwise the test degenerates into a walkover.
	okT, qT, _ := Match("se", "session_data")
	okK, qK, _ := Match("se", "SELECT")
	if !okT || !okK {
		t.Fatalf("precondition: Match(se,session_data)=%v Match(se,SELECT)=%v; both must be ok", okT, okK)
	}
	if qT != qK {
		t.Fatalf("precondition: match qualities differ (table %d vs keyword %d); test must compete at EQUAL quality", qT, qK)
	}

	if got[0].Source != SchemaSourceName {
		t.Fatalf("top suggestion Source = %q (text %q); want schema table to outrank keyword via 80>40 bias",
			got[0].Source, got[0].Text)
	}

	// A keyword must ALSO survive and rank below the schema entry so this is a
	// real competition: schema wins the bias tie-break rather than being alone.
	keywordSurvived, keywordRankedLower := false, false
	for i, sg := range got {
		if sg.Source != KeywordsSourceName {
			continue
		}
		keywordSurvived = true
		if i > 0 {
			keywordRankedLower = true
		}
	}
	if !keywordSurvived {
		t.Fatalf("no keyword survived; test is a walkover, not a competition (got %v)", suggestionTexts(got))
	}
	if !keywordRankedLower {
		t.Fatalf("a keyword tied/beat the schema table at got[0]; schema bias did not win (got %v)", suggestionTexts(got))
	}
}

// TestEngine_SchemaColumnsOutrankKeywords mirrors the tables case for COLUMN
// suggestions. Reconciled from "schema constant Score=3
// always wins" to "schema column outranks keyword AT COMPARABLE match quality"
// (SchemaSourceBias 80 > KeywordSourceBias 40). The dot-qualified prefix "se"
// fuzzily matches both the column "session_token" and the keywords SELECT/SET
// at identical quality; the column reaches got[0] only via the bias tie-break,
// and a keyword must also survive and rank lower for a genuine competition.
func TestEngine_SchemaColumnsOutrankKeywords(t *testing.T) {
	m := newFakeMeta()
	m.setColumns("app", "users", models.Column{Name: "session_token"})
	schema := NewSchemaSource(m, &fakeWarmer{}, schemaProv("app"))
	eng := NewEngine([]Source{KeywordsSource{PriorityVal: 20}, schema})

	// The `users.se` qualifier resolves columns under the active schema "app";
	// the partial "se" competes against keywords at equal match quality.
	b, p := bufWithCursor("SELECT users.se")
	got := eng.Trigger(context.Background(), b, p)
	if len(got) == 0 {
		t.Fatal("Trigger returned no suggestions")
	}

	okC, qC, _ := Match("se", "session_token")
	okK, qK, _ := Match("se", "SELECT")
	if !okC || !okK {
		t.Fatalf("precondition: Match(se,session_token)=%v Match(se,SELECT)=%v; both must be ok", okC, okK)
	}
	if qC != qK {
		t.Fatalf("precondition: match qualities differ (column %d vs keyword %d); must compete at EQUAL quality", qC, qK)
	}

	if got[0].Source != SchemaSourceName {
		t.Fatalf("top suggestion Source = %q (text %q); want schema column to outrank keyword via 80>40 bias",
			got[0].Source, got[0].Text)
	}

	keywordSurvived, keywordRankedLower := false, false
	for i, sg := range got {
		if sg.Source != KeywordsSourceName {
			continue
		}
		keywordSurvived = true
		if i > 0 {
			keywordRankedLower = true
		}
	}
	if !keywordSurvived {
		t.Fatalf("no keyword survived; test is a walkover, not a competition (got %v)", suggestionTexts(got))
	}
	if !keywordRankedLower {
		t.Fatalf("a keyword tied/beat the schema column at got[0]; schema bias did not win (got %v)", suggestionTexts(got))
	}
}
