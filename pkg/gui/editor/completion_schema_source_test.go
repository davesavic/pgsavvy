package editor

import (
	"context"
	"errors"
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
}

func (f *fakeSession) ListTables(_ context.Context, schema string) ([]*models.Table, error) {
	f.gotListTablesSchema = schema
	return f.tables, f.tablesErr
}

func (f *fakeSession) ListColumns(_ context.Context, schema, table string) ([]models.Column, error) {
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
