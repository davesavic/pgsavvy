package sqlcontext

import (
	"testing"
)

// findTable returns the first TableRef with the given alias, or false.
func findByAlias(refs []TableRef, alias string) (TableRef, bool) {
	for _, r := range refs {
		if r.Alias == alias {
			return r, true
		}
	}
	return TableRef{}, false
}

// hasTableName reports whether any TableRef carries the given bare Name —
// used to assert an alias never leaks in as a table name.
func hasTableName(refs []TableRef, name string) bool {
	for _, r := range refs {
		if r.Name == name {
			return true
		}
	}
	return false
}

// AC Rule: InScopeTables lists each table with its alias and schema; an
// alias is never emitted as a table name.
func TestSQLContextInScopeTablesNoAliasPollution(t *testing.T) {
	sql := "SELECT * FROM users u WHERE u."
	got := Analyze(sql, runeLen(sql))

	ref, ok := findByAlias(got.InScopeTables, "u")
	if !ok {
		t.Fatalf("expected a table with alias u, got %+v", got.InScopeTables)
	}
	if ref.Name != "users" {
		t.Fatalf("alias u should resolve to users, got %q", ref.Name)
	}
	// The alias "u" must NOT appear as a table name.
	if hasTableName(got.InScopeTables, "u") {
		t.Fatalf("alias u leaked into table-name set: %+v", got.InScopeTables)
	}
}

// AC Scenario 1 + Rule: a trailing "alias." sets Qualifier to the
// alias's resolved table; Expect=Columns.
func TestSQLContextAliasDotResolvesTable(t *testing.T) {
	sql := "SELECT * FROM users u WHERE u."
	got := Analyze(sql, runeLen(sql))

	if !got.Qualifier.Present {
		t.Fatalf("expected a present Qualifier, got %+v", got.Qualifier)
	}
	if got.Qualifier.Ident != "u" {
		t.Fatalf("Qualifier.Ident = %q, want u", got.Qualifier.Ident)
	}
	if got.Qualifier.Table != "users" {
		t.Fatalf("Qualifier.Table = %q, want users", got.Qualifier.Table)
	}
	if got.Expect != ExpectColumns {
		t.Fatalf("Expect = %v, want ExpectColumns", got.Expect)
	}
}

// AC Scenario 2: self-join with distinct aliases u1 and u2 both -> users;
// Qualifier resolves u2 -> users.
func TestSQLContextSelfJoinDistinctAliases(t *testing.T) {
	sql := "SELECT * FROM users u1 JOIN users u2 ON u2."
	got := Analyze(sql, runeLen(sql))

	r1, ok1 := findByAlias(got.InScopeTables, "u1")
	r2, ok2 := findByAlias(got.InScopeTables, "u2")
	if !ok1 || !ok2 {
		t.Fatalf("expected aliases u1 and u2, got %+v", got.InScopeTables)
	}
	if r1.Name != "users" || r2.Name != "users" {
		t.Fatalf("both aliases should map to users, got u1=%q u2=%q", r1.Name, r2.Name)
	}
	if got.Qualifier.Table != "users" || got.Qualifier.Ident != "u2" {
		t.Fatalf("Qualifier = %+v, want Ident=u2 Table=users", got.Qualifier)
	}
}

// AC Scenario 3 + Rules (schema-qualified + quoted): 'public."Orders" o'
// yields {Schema:public, Name:Orders, Alias:o}; o. resolves -> Orders.
func TestSQLContextSchemaQualifiedQuotedTable(t *testing.T) {
	sql := `SELECT * FROM public."Orders" o WHERE o.`
	got := Analyze(sql, runeLen(sql))

	ref, ok := findByAlias(got.InScopeTables, "o")
	if !ok {
		t.Fatalf("expected alias o, got %+v", got.InScopeTables)
	}
	if ref.Schema != "public" {
		t.Fatalf("Schema = %q, want public", ref.Schema)
	}
	// Quoted identifier resolves to its UNQUOTED value preserving case.
	if ref.Name != "Orders" {
		t.Fatalf("Name = %q, want Orders (unquoted, case-preserved)", ref.Name)
	}
	if got.Qualifier.Table != "Orders" || got.Qualifier.Schema != "public" {
		t.Fatalf("Qualifier = %+v, want Table=Orders Schema=public", got.Qualifier)
	}
}

// AC Rule: schema-qualified "schema.table" resolves Schema and Name
// separately (bare, no quotes, no alias).
func TestSQLContextSchemaQualifiedBare(t *testing.T) {
	sql := "SELECT * FROM public.users"
	got := Analyze(sql, runeLen(sql))

	if len(got.InScopeTables) != 1 {
		t.Fatalf("expected one table, got %+v", got.InScopeTables)
	}
	ref := got.InScopeTables[0]
	if ref.Schema != "public" || ref.Name != "users" || ref.Alias != "" {
		t.Fatalf("ref = %+v, want {Schema:public Name:users Alias:}", ref)
	}
	// "public" is the schema, not a table name.
	if hasTableName(got.InScopeTables, "public") {
		t.Fatalf("schema public leaked as a table name: %+v", got.InScopeTables)
	}
}

// AC Rule: bare quoted table resolves to unquoted case-preserved value.
func TestSQLContextQuotedTableUnquoted(t *testing.T) {
	sql := `SELECT * FROM "MyTable"`
	got := Analyze(sql, runeLen(sql))

	if len(got.InScopeTables) != 1 {
		t.Fatalf("expected one table, got %+v", got.InScopeTables)
	}
	if got.InScopeTables[0].Name != "MyTable" {
		t.Fatalf("Name = %q, want MyTable (not the literal with quotes)", got.InScopeTables[0].Name)
	}
}

// AC Edge: "FROM users AS u" and "FROM users u" both yield alias u.
func TestSQLContextExplicitAndImplicitAs(t *testing.T) {
	for _, sql := range []string{
		"SELECT * FROM users AS u WHERE u.",
		"SELECT * FROM users u WHERE u.",
	} {
		got := Analyze(sql, runeLen(sql))
		ref, ok := findByAlias(got.InScopeTables, "u")
		if !ok || ref.Name != "users" {
			t.Fatalf("%q: expected alias u -> users, got %+v", sql, got.InScopeTables)
		}
		if got.Qualifier.Table != "users" {
			t.Fatalf("%q: Qualifier.Table = %q, want users", sql, got.Qualifier.Table)
		}
	}
}

// AC Edge: duplicate alias collision must not crash; resolution is
// deterministic (last-wins — the alias resolves to the most recently
// parsed table bearing it).
func TestSQLContextDuplicateAliasDeterministic(t *testing.T) {
	sql := "SELECT * FROM a x JOIN b x ON x."
	got := Analyze(sql, runeLen(sql)) // must not panic

	// Both tables are still in scope, each with alias x.
	if len(got.InScopeTables) != 2 {
		t.Fatalf("expected two in-scope tables, got %+v", got.InScopeTables)
	}
	// Last-wins: the dot qualifier x resolves to table b (the later one).
	if got.Qualifier.Table != "b" {
		t.Fatalf("Qualifier.Table = %q, want b (last-wins)", got.Qualifier.Table)
	}
}

// AC Rule + Edge: undeclared-alias dot ("z.") yields a present Qualifier
// with an empty resolved table (no panic, no wrong table); Expect=Columns.
func TestSQLContextUndeclaredAliasDot(t *testing.T) {
	sql := "SELECT * FROM users u WHERE z."
	got := Analyze(sql, runeLen(sql)) // must not panic

	if !got.Qualifier.Present {
		t.Fatalf("expected Qualifier present for typed z., got %+v", got.Qualifier)
	}
	if got.Qualifier.Ident != "z" {
		t.Fatalf("Qualifier.Ident = %q, want z", got.Qualifier.Ident)
	}
	if got.Qualifier.Table != "" {
		t.Fatalf("Qualifier.Table = %q, want empty (undeclared alias)", got.Qualifier.Table)
	}
	if got.Expect != ExpectColumns {
		t.Fatalf("Expect = %v, want ExpectColumns", got.Expect)
	}
}

// A dot qualifier may also resolve via a bare table name (no alias),
// e.g. "FROM users WHERE users.".
func TestSQLContextBareTableNameDot(t *testing.T) {
	sql := "SELECT * FROM users WHERE users."
	got := Analyze(sql, runeLen(sql))

	if got.Qualifier.Table != "users" {
		t.Fatalf("Qualifier.Table = %q, want users (bare table-name fallback)", got.Qualifier.Table)
	}
}

// No trailing dot at the cursor => Qualifier absent (zero value).
func TestSQLContextNoQualifierWithoutDot(t *testing.T) {
	sql := "SELECT * FROM users u WHERE u"
	got := Analyze(sql, runeLen(sql))

	if got.Qualifier.Present {
		t.Fatalf("expected no Qualifier without a trailing dot, got %+v", got.Qualifier)
	}
}
