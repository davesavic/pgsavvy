package editor

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// fakeSession is a minimal drivers.Session that records the
// schema/table args passed to ListTables / ListColumns and returns
// the configured tables/cols (or errors). Every other Session
// method panics — SchemaSource must only call these two.
type fakeSession struct {
	drivers.Session // embed to satisfy the interface for unused methods

	tables    []*models.Table
	tablesErr error

	cols    []models.Column
	colsErr error

	gotListTablesSchema string
	gotListColumnsArgs  [2]string

	listTablesCalls  int
	listColumnsCalls int
}

func (f *fakeSession) ListTables(_ context.Context, schema string) ([]*models.Table, error) {
	f.listTablesCalls++
	f.gotListTablesSchema = schema
	return f.tables, f.tablesErr
}

func (f *fakeSession) ListColumns(_ context.Context, schema, table string) ([]models.Column, error) {
	f.listColumnsCalls++
	f.gotListColumnsArgs = [2]string{schema, table}
	return f.cols, f.colsErr
}

func bufWithCursor(line string) (*Buffer, Position) {
	b := bufFromLines(line)
	return b, Position{Line: 0, Col: len([]rune(line))}
}

func sessProv(s drivers.Session) SessionProvider { return func() drivers.Session { return s } }
func schemaProv(s string) SchemaProvider         { return func() string { return s } }

func TestSchemaSource_Identity(t *testing.T) {
	src := NewSchemaSource(nil, nil)
	if src.Name() != SchemaSourceName {
		t.Errorf("Name() = %q; want %q", src.Name(), SchemaSourceName)
	}
	if src.Priority() != SchemaSourcePriority {
		t.Errorf("Priority() = %d; want %d", src.Priority(), SchemaSourcePriority)
	}
}

func TestSchemaSource_NoProviders_Empty(t *testing.T) {
	src := NewSchemaSource(nil, nil)
	b, p := bufWithCursor("SELECT * FROM ")
	got := src.Suggest(context.Background(), b, p)
	if got == nil || len(got) != 0 {
		t.Fatalf("Suggest with nil providers = %+v; want empty non-nil", got)
	}
}

func TestSchemaSource_NoActiveSession_Empty(t *testing.T) {
	src := NewSchemaSource(sessProv(nil), schemaProv("public"))
	b, p := bufWithCursor("SELECT * FROM ")
	got := src.Suggest(context.Background(), b, p)
	if len(got) != 0 {
		t.Fatalf("Suggest with nil session = %+v; want empty", got)
	}
}

func TestSchemaSource_NilBuffer_Empty(t *testing.T) {
	src := NewSchemaSource(nil, nil)
	got := src.Suggest(context.Background(), nil, Position{})
	if got == nil || len(got) != 0 {
		t.Fatalf("Suggest(nil buffer) = %+v; want empty", got)
	}
}

func TestSchemaSource_TableContexts(t *testing.T) {
	tables := []*models.Table{
		{Name: "users"},
		{Name: "orders"},
	}
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
			sess := &fakeSession{tables: tables}
			src := NewSchemaSource(sessProv(sess), schemaProv("public"))
			b, p := bufWithCursor(tc.line)
			got := src.Suggest(context.Background(), b, p)
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
				if sess.gotListTablesSchema != "public" {
					t.Errorf("ListTables schema = %q; want public", sess.gotListTablesSchema)
				}
			} else {
				if len(got) != 0 {
					t.Fatalf("len(got) = %d; want 0 for line %q (no match expected)", len(got), tc.line)
				}
			}
		})
	}
}

func TestSchemaSource_ColumnContext(t *testing.T) {
	cols := []models.Column{
		{Name: "id", DataType: "integer"},
		{Name: "email", DataType: "text"},
	}
	tests := []struct {
		name      string
		line      string
		wantHit   bool
		wantTable string
	}{
		{"users.", "SELECT users.", true, "users"},
		{"FROM users JOIN orders ... users.", "SELECT * FROM users WHERE users.", true, "users"},
		{"mixed-case Users.", "SELECT Users.", true, "Users"},
		{"space before dot (no match)", "SELECT users .", false, ""},
		{"two dots (no match — schema-qualified v1 not supported)", "SELECT public.users.", true, "users"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sess := &fakeSession{cols: cols}
			src := NewSchemaSource(sessProv(sess), schemaProv("public"))
			b, p := bufWithCursor(tc.line)
			got := src.Suggest(context.Background(), b, p)
			if !tc.wantHit {
				if len(got) != 0 {
					t.Fatalf("len(got) = %d; want 0 for line %q", len(got), tc.line)
				}
				return
			}
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
			if sess.gotListColumnsArgs[1] != tc.wantTable {
				t.Errorf("ListColumns table = %q; want %q", sess.gotListColumnsArgs[1], tc.wantTable)
			}
		})
	}
}

func TestSchemaSource_ColumnDisplayFallsBackWithoutType(t *testing.T) {
	sess := &fakeSession{cols: []models.Column{{Name: "id"}}}
	src := NewSchemaSource(sessProv(sess), schemaProv("public"))
	b, p := bufWithCursor("SELECT users.")
	got := src.Suggest(context.Background(), b, p)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d; want 1", len(got))
	}
	if got[0].Display != "id" {
		t.Errorf("Display = %q; want %q (no type → no separator)", got[0].Display, "id")
	}
}

func TestSchemaSource_InsideStringLiteral_Empty(t *testing.T) {
	tables := []*models.Table{{Name: "users"}}
	sess := &fakeSession{tables: tables}
	src := NewSchemaSource(sessProv(sess), schemaProv("public"))
	tests := []string{
		"SELECT 'SELECT * FROM ",   // unclosed single-quoted
		"SELECT 'a' || 'and FROM ", // second open string
		"SELECT $$ outer FROM ",    // dollar-quoted untagged
		"SELECT $tag$inner FROM ",  // dollar-quoted tagged unclosed
	}
	for _, line := range tests {
		t.Run(line, func(t *testing.T) {
			b, p := bufWithCursor(line)
			got := src.Suggest(context.Background(), b, p)
			if len(got) != 0 {
				t.Fatalf("len(got) = %d; want 0 for %q", len(got), line)
			}
		})
	}
}

func TestSchemaSource_InsideLineComment_Empty(t *testing.T) {
	tables := []*models.Table{{Name: "users"}}
	sess := &fakeSession{tables: tables}
	src := NewSchemaSource(sessProv(sess), schemaProv("public"))
	b, p := bufWithCursor("SELECT 1 -- FROM ")
	got := src.Suggest(context.Background(), b, p)
	if len(got) != 0 {
		t.Fatalf("len(got) = %d; want 0 (inside -- comment)", len(got))
	}
}

func TestSchemaSource_InsideBlockComment_Empty(t *testing.T) {
	tables := []*models.Table{{Name: "users"}}
	sess := &fakeSession{tables: tables}
	src := NewSchemaSource(sessProv(sess), schemaProv("public"))
	// Unclosed block comment: FROM trigger is masked through end of line.
	b, p := bufWithCursor("SELECT 1 /* FROM ")
	got := src.Suggest(context.Background(), b, p)
	if len(got) != 0 {
		t.Fatalf("len(got) = %d; want 0 (inside /* block)", len(got))
	}
}

func TestSchemaSource_ClosedBlockComment_StillTriggers(t *testing.T) {
	tables := []*models.Table{{Name: "users"}}
	sess := &fakeSession{tables: tables}
	src := NewSchemaSource(sessProv(sess), schemaProv("public"))
	b, p := bufWithCursor("SELECT 1 /* nope */ FROM ")
	got := src.Suggest(context.Background(), b, p)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d; want 1 (FROM after closed /* */)", len(got))
	}
}

func TestSchemaSource_EscapedQuotesInString(t *testing.T) {
	tables := []*models.Table{{Name: "users"}}
	sess := &fakeSession{tables: tables}
	src := NewSchemaSource(sessProv(sess), schemaProv("public"))
	// '' is an escaped single quote inside the literal; FROM is still
	// inside the string so no trigger.
	b, p := bufWithCursor("SELECT 'it''s FROM ")
	got := src.Suggest(context.Background(), b, p)
	if len(got) != 0 {
		t.Fatalf("len(got) = %d; want 0 (FROM inside escaped string)", len(got))
	}
	// Closed string then FROM: should trigger.
	b2, p2 := bufWithCursor("SELECT 'it''s ok' FROM ")
	got2 := src.Suggest(context.Background(), b2, p2)
	if len(got2) != 1 {
		t.Fatalf("len(got2) = %d; want 1 (FROM after closed string)", len(got2))
	}
}

func TestSchemaSource_EmptySchema_Empty(t *testing.T) {
	tables := []*models.Table{{Name: "users"}}
	sess := &fakeSession{tables: tables}
	src := NewSchemaSource(sessProv(sess), schemaProv(""))
	b, p := bufWithCursor("SELECT * FROM ")
	got := src.Suggest(context.Background(), b, p)
	if len(got) != 0 {
		t.Fatalf("len(got) = %d; want 0 (empty schema)", len(got))
	}
}

func TestSchemaSource_ZeroTables_Empty(t *testing.T) {
	sess := &fakeSession{tables: nil}
	src := NewSchemaSource(sessProv(sess), schemaProv("public"))
	b, p := bufWithCursor("SELECT * FROM ")
	got := src.Suggest(context.Background(), b, p)
	if got == nil || len(got) != 0 {
		t.Fatalf("len(got) = %d; want empty for zero tables", len(got))
	}
}

func TestSchemaSource_DriverError_Empty(t *testing.T) {
	sess := &fakeSession{tablesErr: errors.New("boom")}
	src := NewSchemaSource(sessProv(sess), schemaProv("public"))
	b, p := bufWithCursor("SELECT * FROM ")
	got := src.Suggest(context.Background(), b, p)
	if len(got) != 0 {
		t.Fatalf("len(got) = %d; want 0 on driver error", len(got))
	}
}

func TestSchemaSource_MissingTable_Empty(t *testing.T) {
	// ListColumns returns zero cols for a typo'd table.
	sess := &fakeSession{cols: nil}
	src := NewSchemaSource(sessProv(sess), schemaProv("public"))
	b, p := bufWithCursor("SELECT usres.")
	got := src.Suggest(context.Background(), b, p)
	if len(got) != 0 {
		t.Fatalf("len(got) = %d; want 0 for typo'd table", len(got))
	}
	if sess.gotListColumnsArgs[1] != "usres" {
		t.Errorf("ListColumns table = %q; want passed verbatim 'usres'", sess.gotListColumnsArgs[1])
	}
}

func TestSchemaSource_PreservesIdentifierCase(t *testing.T) {
	sess := &fakeSession{cols: []models.Column{{Name: "id", DataType: "int"}}}
	src := NewSchemaSource(sessProv(sess), schemaProv("public"))
	b, p := bufWithCursor("SELECT MyTable.")
	_ = src.Suggest(context.Background(), b, p)
	if sess.gotListColumnsArgs[1] != "MyTable" {
		t.Errorf("ListColumns table = %q; want preserved 'MyTable'", sess.gotListColumnsArgs[1])
	}
}

func TestSchemaSource_CursorMidLine_UsesLineUpToCursor(t *testing.T) {
	tables := []*models.Table{{Name: "users"}}
	sess := &fakeSession{tables: tables}
	src := NewSchemaSource(sessProv(sess), schemaProv("public"))
	// Cursor sits right after "FROM " in "SELECT * FROM ; -- trailing".
	full := "SELECT * FROM ; -- trailing"
	b := bufFromLines(full)
	p := Position{Line: 0, Col: len([]rune("SELECT * FROM "))}
	got := src.Suggest(context.Background(), b, p)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d; want 1 (line-up-to-cursor ends at FROM ' ')", len(got))
	}
}

func TestStripNoise_BasicCases(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"FROM ", "FROM "},
		{"'abc' FROM ", "      FROM "},
		{"'a''b' FROM ", "       FROM "},
		{"-- FROM x", "         "},
		{"/* x */ FROM ", "        FROM "},
		{"$$x$$ FROM ", "      FROM "},
		{"$tag$x$tag$ FROM ", "            FROM "},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got := stripNoise(tc.in)
			if got != tc.want {
				t.Errorf("stripNoise(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestSchemaSource_JoinOnContext covers the JOIN ... ON ... condition
// position: the source should offer the tables already in scope (so the
// user can qualify a column via `posts.`) plus those tables' columns.
func TestSchemaSource_JoinOnContext(t *testing.T) {
	sess := &fakeSession{
		tables: []*models.Table{{Name: "posts"}, {Name: "posts_summary"}},
		cols:   []models.Column{{Name: "id"}, {Name: "post_id"}},
	}
	src := NewSchemaSource(sessProv(sess), schemaProv("public"))

	// Empty partial right after `ON `: every scoped table plus columns.
	b, p := bufWithCursor("select * from posts join posts_summary on ")
	got := texts(src.Suggest(context.Background(), b, p))
	for _, want := range []string{"posts", "posts_summary", "id", "post_id"} {
		if !slices.Contains(got, want) {
			t.Errorf("ON-context (empty partial) missing %q; got %v", want, got)
		}
	}

	// Partial `posts` (the screenshot): scoped tables matching the prefix.
	b2, p2 := bufWithCursor("select * from posts join posts_summary on posts")
	got2 := texts(src.Suggest(context.Background(), b2, p2))
	if !slices.Contains(got2, "posts") || !slices.Contains(got2, "posts_summary") {
		t.Errorf("ON-context prefix `posts` should offer posts/posts_summary; got %v", got2)
	}

	// USING is also a join-condition keyword.
	b3, p3 := bufWithCursor("select * from posts join posts_summary using ")
	if len(src.Suggest(context.Background(), b3, p3)) == 0 {
		t.Error("USING context returned no suggestions")
	}
}

func texts(sugs []Suggestion) []string {
	out := make([]string, 0, len(sugs))
	for _, s := range sugs {
		out = append(out, s.Text)
	}
	return out
}

// TestSchemaSource_TablePrefixFilter covers prefix-narrowing of the
// table list: empty partial → all, partial → case-insensitive prefix
// filter, no-match → empty.
func TestSchemaSource_TablePrefixFilter(t *testing.T) {
	tables := []*models.Table{{Name: "users"}, {Name: "usage"}, {Name: "orders"}}
	tests := []struct {
		name string
		line string
		want []string
	}{
		{"empty partial returns all", "SELECT * FROM ", []string{"users", "usage", "orders"}},
		{"partial us narrows", "SELECT * FROM us", []string{"users", "usage"}},
		{"case-insensitive partial US", "SELECT * FROM US", []string{"users", "usage"}},
		{"no-match partial zz empty", "SELECT * FROM zz", nil},
		{"full name then space does not re-offer", "SELECT * FROM users ", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sess := &fakeSession{tables: tables}
			src := NewSchemaSource(sessProv(sess), schemaProv("public"))
			b, p := bufWithCursor(tc.line)
			got := texts(src.Suggest(context.Background(), b, p))
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

// TestSchemaSource_ColumnPrefixFilter covers prefix-narrowing of the
// column list after `<ident>.`.
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
			sess := &fakeSession{cols: cols}
			src := NewSchemaSource(sessProv(sess), schemaProv("public"))
			b, p := bufWithCursor(tc.line)
			got := texts(src.Suggest(context.Background(), b, p))
			if len(got) != len(tc.want) {
				t.Fatalf("Suggest(%q) = %v; want %v", tc.line, got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("Suggest(%q) = %v; want %v", tc.line, got, tc.want)
				}
			}
			if sess.gotListColumnsArgs[1] != "users" {
				t.Errorf("ListColumns table = %q; want users", sess.gotListColumnsArgs[1])
			}
		})
	}
}

// TestSchemaSource_TableCache verifies the table list is fetched at
// most once per session identity across repeated/refiltering Suggests.
func TestSchemaSource_TableCache(t *testing.T) {
	sess := &fakeSession{tables: []*models.Table{{Name: "users"}, {Name: "usage"}}}
	src := NewSchemaSource(sessProv(sess), schemaProv("public"))

	for _, line := range []string{"SELECT * FROM ", "SELECT * FROM u", "SELECT * FROM us"} {
		b, p := bufWithCursor(line)
		src.Suggest(context.Background(), b, p)
	}
	if sess.listTablesCalls != 1 {
		t.Fatalf("listTablesCalls = %d; want 1 (cached)", sess.listTablesCalls)
	}
}

// TestSchemaSource_ColumnCachePerTable verifies columns are cached
// keyed by table so `users.` and `orders.` don't collide, and a
// repeated lookup on the same table does not re-query.
func TestSchemaSource_ColumnCachePerTable(t *testing.T) {
	sess := &perTableSession{
		colsByTable: map[string][]models.Column{
			"users":  {{Name: "uid"}},
			"orders": {{Name: "oid"}},
		},
	}
	src := NewSchemaSource(sessProv(sess), schemaProv("public"))

	first := texts(suggestLine(src, "SELECT users."))
	if len(first) != 1 || first[0] != "uid" {
		t.Fatalf("users. = %v; want [uid]", first)
	}
	second := texts(suggestLine(src, "SELECT orders."))
	if len(second) != 1 || second[0] != "oid" {
		t.Fatalf("orders. = %v; want [oid] (per-table cache, no collision)", second)
	}
	// Re-query users. — must hit cache, not re-call.
	again := texts(suggestLine(src, "SELECT users."))
	if len(again) != 1 || again[0] != "uid" {
		t.Fatalf("users. (again) = %v; want [uid]", again)
	}
	if sess.callsByTable["users"] != 1 {
		t.Errorf("ListColumns(users) calls = %d; want 1", sess.callsByTable["users"])
	}
	if sess.callsByTable["orders"] != 1 {
		t.Errorf("ListColumns(orders) calls = %d; want 1", sess.callsByTable["orders"])
	}
}

// TestSchemaSource_SessionSwapInvalidatesCache verifies a different
// Session pointer triggers a refetch.
func TestSchemaSource_SessionSwapInvalidatesCache(t *testing.T) {
	sessA := &fakeSession{tables: []*models.Table{{Name: "alpha"}}}
	sessB := &fakeSession{tables: []*models.Table{{Name: "beta"}}}
	current := drivers.Session(sessA)
	src := NewSchemaSource(func() drivers.Session { return current }, schemaProv("public"))

	got := texts(suggestLine(src, "SELECT * FROM "))
	if len(got) != 1 || got[0] != "alpha" {
		t.Fatalf("got %v; want [alpha]", got)
	}
	current = sessB
	got = texts(suggestLine(src, "SELECT * FROM "))
	if len(got) != 1 || got[0] != "beta" {
		t.Fatalf("after swap got %v; want [beta]", got)
	}
	if sessB.listTablesCalls != 1 {
		t.Errorf("sessB listTablesCalls = %d; want 1 (refetch on swap)", sessB.listTablesCalls)
	}
}

// TestSchemaSource_TableErrorNotCached verifies a failed ListTables is
// not cached and the next Suggest retries.
func TestSchemaSource_TableErrorNotCached(t *testing.T) {
	sess := &fakeSession{tablesErr: errors.New("boom")}
	src := NewSchemaSource(sessProv(sess), schemaProv("public"))

	if got := suggestLine(src, "SELECT * FROM "); len(got) != 0 {
		t.Fatalf("got %v; want empty on error", got)
	}
	// Recover and retry — must re-query (error wasn't cached).
	sess.tablesErr = nil
	sess.tables = []*models.Table{{Name: "users"}}
	if got := texts(suggestLine(src, "SELECT * FROM ")); len(got) != 1 || got[0] != "users" {
		t.Fatalf("after recovery got %v; want [users]", got)
	}
	if sess.listTablesCalls != 2 {
		t.Errorf("listTablesCalls = %d; want 2 (retry after uncached error)", sess.listTablesCalls)
	}
}

func suggestLine(src *SchemaSource, line string) []Suggestion {
	b, p := bufWithCursor(line)
	return src.Suggest(context.Background(), b, p)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSchemaSource_UnqualifiedColumns_AfterWhere covers the headline
// case: after `WHERE ` the columns of the FROM table are suggested even
// though no `<table>.` qualifier was typed.
func TestSchemaSource_UnqualifiedColumns_AfterWhere(t *testing.T) {
	sess := &fakeSession{cols: []models.Column{{Name: "id", DataType: "int"}, {Name: "title", DataType: "text"}}}
	src := NewSchemaSource(sessProv(sess), schemaProv("public"))
	got := texts(suggestLine(src, "SELECT * FROM posts WHERE "))
	if !equalStrings(got, []string{"id", "title"}) {
		t.Fatalf("got %v; want [id title]", got)
	}
	if sess.gotListColumnsArgs[1] != "posts" {
		t.Errorf("ListColumns table = %q; want posts", sess.gotListColumnsArgs[1])
	}
}

// TestSchemaSource_UnqualifiedColumns_AfterSelectWithLaterFrom proves
// scope resolution scans the whole statement, not just text up to the
// cursor: the cursor sits after `SELECT ` but the FROM clause that names
// the table appears later in the buffer.
func TestSchemaSource_UnqualifiedColumns_AfterSelectWithLaterFrom(t *testing.T) {
	sess := &fakeSession{cols: []models.Column{{Name: "id"}, {Name: "name"}}}
	src := NewSchemaSource(sessProv(sess), schemaProv("public"))
	full := "SELECT  FROM users"
	b := bufFromLines(full)
	p := Position{Line: 0, Col: len([]rune("SELECT "))}
	got := texts(src.Suggest(context.Background(), b, p))
	if !equalStrings(got, []string{"id", "name"}) {
		t.Fatalf("got %v; want [id name]", got)
	}
}

// TestSchemaSource_UnqualifiedColumns_OperatorAndKeywordContexts covers
// every trigger position in scope: after WHERE, after a comparison
// operator, and after AND.
func TestSchemaSource_UnqualifiedColumns_OperatorAndKeywordContexts(t *testing.T) {
	for _, line := range []string{
		"SELECT * FROM posts WHERE ",
		"SELECT * FROM posts WHERE id = ",
		"SELECT * FROM posts WHERE id = 1 AND ",
		"SELECT * FROM posts WHERE id = 1 OR ",
	} {
		sess := &fakeSession{cols: []models.Column{{Name: "id"}}}
		src := NewSchemaSource(sessProv(sess), schemaProv("public"))
		got := texts(suggestLine(src, line))
		if len(got) != 1 || got[0] != "id" {
			t.Fatalf("line %q -> %v; want [id]", line, got)
		}
	}
}

// TestSchemaSource_UnqualifiedColumns_PrefixFilter narrows the column
// list by the partial identifier the user has begun typing.
func TestSchemaSource_UnqualifiedColumns_PrefixFilter(t *testing.T) {
	sess := &fakeSession{cols: []models.Column{{Name: "id"}, {Name: "email"}, {Name: "events"}}}
	src := NewSchemaSource(sessProv(sess), schemaProv("public"))
	got := texts(suggestLine(src, "SELECT * FROM users WHERE e"))
	if !equalStrings(got, []string{"email", "events"}) {
		t.Fatalf("got %v; want [email events]", got)
	}
}

// TestSchemaSource_UnqualifiedColumns_MultiTableUnionDedupe verifies the
// FROM table and every JOIN table contribute columns, unioned in scope
// order and deduplicated by name (a column shared by two tables appears
// once).
func TestSchemaSource_UnqualifiedColumns_MultiTableUnionDedupe(t *testing.T) {
	sess := &perTableSession{colsByTable: map[string][]models.Column{
		"users":  {{Name: "id"}, {Name: "email"}},
		"orders": {{Name: "id"}, {Name: "total"}},
	}}
	src := NewSchemaSource(sessProv(sess), schemaProv("public"))
	got := texts(suggestLine(src, "SELECT * FROM users u JOIN orders o ON u.id = o.user_id WHERE "))
	if !equalStrings(got, []string{"id", "email", "total"}) {
		t.Fatalf("got %v; want [id email total] (union, deduped)", got)
	}
}

// TestSchemaSource_UnqualifiedColumns_NoTableInScope_Empty guards the
// no-FROM case: a column position with no resolvable table yields no
// suggestions (rather than every column of every table).
func TestSchemaSource_UnqualifiedColumns_NoTableInScope_Empty(t *testing.T) {
	sess := &fakeSession{cols: []models.Column{{Name: "id"}}}
	src := NewSchemaSource(sessProv(sess), schemaProv("public"))
	if got := suggestLine(src, "SELECT "); len(got) != 0 {
		t.Fatalf("got %v; want empty (no FROM table in scope)", got)
	}
}

// TestSchemaSource_ColumnContext_InsideStringSuppressed verifies the
// cursor sitting inside an unterminated string literal suppresses
// column suggestions even though the stripped line looks like a WHERE
// column position.
func TestSchemaSource_ColumnContext_InsideStringSuppressed(t *testing.T) {
	sess := &fakeSession{cols: []models.Column{{Name: "id"}}}
	src := NewSchemaSource(sessProv(sess), schemaProv("public"))
	if got := suggestLine(src, "SELECT * FROM users WHERE 'abc"); len(got) != 0 {
		t.Fatalf("got %v; want empty (cursor inside string literal)", got)
	}
}

// perTableSession returns columns keyed by table name and records the
// per-table call count to prove the per-(schema,table) cache shape.
type perTableSession struct {
	drivers.Session

	colsByTable  map[string][]models.Column
	callsByTable map[string]int
}

func (f *perTableSession) ListColumns(_ context.Context, _, table string) ([]models.Column, error) {
	if f.callsByTable == nil {
		f.callsByTable = map[string]int{}
	}
	f.callsByTable[table]++
	return f.colsByTable[table], nil
}

// TestEngine_SchemaTablesOutrankKeywords regresses dbsavvy-ybi: schema
// tables are the most relevant completion in a FROM context, yet they
// were emitted with Score=0 while KeywordsSource emits Score=1. Engine
// sorts Score-descending, so every keyword buried the tables below the
// visible window. The documented intent (static_sources.go: "keywords
// lose to richer sources") requires schema suggestions to outrank
// keywords. The first result for "SELECT * FROM " must be the schema
// table, not a keyword.
func TestEngine_SchemaTablesOutrankKeywords(t *testing.T) {
	sess := &fakeSession{tables: []*models.Table{{Name: "users"}}}
	schema := NewSchemaSource(sessProv(sess), schemaProv("app"))
	eng := NewEngine([]Source{KeywordsSource{PriorityVal: 20}, schema})

	b, p := bufWithCursor("SELECT * FROM ")
	got := eng.Trigger(context.Background(), b, p)
	if len(got) == 0 {
		t.Fatal("Trigger returned no suggestions")
	}
	if got[0].Source != SchemaSourceName {
		t.Fatalf("top suggestion Source = %q (text %q); want schema table to outrank keywords",
			got[0].Source, got[0].Text)
	}
}

// TestEngine_SchemaColumnsOutrankKeywords is the column-context analogue
// of dbsavvy-ybi: after "users." the column list must outrank keywords.
func TestEngine_SchemaColumnsOutrankKeywords(t *testing.T) {
	sess := &fakeSession{cols: []models.Column{{Name: "email"}}}
	schema := NewSchemaSource(sessProv(sess), schemaProv("app"))
	eng := NewEngine([]Source{KeywordsSource{PriorityVal: 20}, schema})

	b, p := bufWithCursor("users.")
	got := eng.Trigger(context.Background(), b, p)
	if len(got) == 0 {
		t.Fatal("Trigger returned no suggestions")
	}
	if got[0].Source != SchemaSourceName {
		t.Fatalf("top suggestion Source = %q (text %q); want schema column to outrank keywords",
			got[0].Source, got[0].Text)
	}
}
