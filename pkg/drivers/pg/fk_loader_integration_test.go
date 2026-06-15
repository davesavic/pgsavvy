//go:build integration

// Integration tests for Session.ListForeignKeys against the docker/postgres
// fixture. Mirrors the openIntegrationSession pattern from
// editability_integration_test.go. Skipped (not failed) when DBSAVVY_TEST_PG
// is unset.

package pg_test

import (
	"context"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/models"
)

func TestListForeignKeys_FixtureUserRoles(t *testing.T) {
	sess := openIntegrationSession(t)
	fks, err := sess.ListForeignKeys(context.Background(), "app", "user_roles")
	if err != nil {
		t.Fatalf("ListForeignKeys: %v", err)
	}
	// Fixture: app.user_roles has two FKs (user_id->users(id) ON DELETE
	// CASCADE; role_id->roles(id) with no override == NO ACTION).
	if len(fks) != 2 {
		t.Fatalf("len(fks) = %d, want 2 (got %+v)", len(fks), fks)
	}
	byRefTable := map[string]models.ForeignKey{}
	for _, fk := range fks {
		byRefTable[fk.RefTable] = fk
	}
	users, ok := byRefTable["users"]
	if !ok {
		t.Fatalf("expected an FK pointing at users; got %+v", fks)
	}
	if users.OnDelete != "CASCADE" {
		t.Errorf("users FK OnDelete = %q, want CASCADE", users.OnDelete)
	}
	if users.OnUpdate != "NO ACTION" {
		t.Errorf("users FK OnUpdate = %q, want NO ACTION", users.OnUpdate)
	}
	if got, want := users.Columns, []string{"user_id"}; !equalStrings(got, want) {
		t.Errorf("users FK Columns = %v, want %v", got, want)
	}
	if got, want := users.RefColumns, []string{"id"}; !equalStrings(got, want) {
		t.Errorf("users FK RefColumns = %v, want %v", got, want)
	}
	if users.Schema != "app" || users.Table != "user_roles" {
		t.Errorf("users FK Schema/Table = %s.%s, want app.user_roles", users.Schema, users.Table)
	}
	if users.RefSchema != "app" {
		t.Errorf("users FK RefSchema = %q, want app", users.RefSchema)
	}

	roles, ok := byRefTable["roles"]
	if !ok {
		t.Fatalf("expected an FK pointing at roles; got %+v", fks)
	}
	if roles.OnDelete != "NO ACTION" {
		t.Errorf("roles FK OnDelete = %q, want NO ACTION", roles.OnDelete)
	}
}

func TestListForeignKeys_TableWithoutFKsReturnsEmpty(t *testing.T) {
	sess := openIntegrationSession(t)
	// app.users is a top-of-graph table with PK + UNIQUE but no FKs.
	fks, err := sess.ListForeignKeys(context.Background(), "app", "users")
	if err != nil {
		t.Fatalf("ListForeignKeys: %v", err)
	}
	if len(fks) != 0 {
		t.Fatalf("expected 0 FKs, got %+v", fks)
	}
	if fks == nil {
		t.Fatalf("expected empty (non-nil) slice; got nil")
	}
}

func TestListForeignKeys_SelfReferenceAndCrossSchemaAndComposite(t *testing.T) {
	sess := openIntegrationSession(t)
	ctx := context.Background()

	// Stand up transient objects so the live fixture stays pristine. Use a
	// dedicated schema we can drop at the end so the test is isolated even
	// if cleanup runs mid-failure.
	stmts := []string{
		`DROP SCHEMA IF EXISTS fkloader_test CASCADE`,
		`DROP SCHEMA IF EXISTS fkloader_test_other CASCADE`,
		`CREATE SCHEMA fkloader_test`,
		`CREATE SCHEMA fkloader_test_other`,
		`CREATE TABLE fkloader_test.tree (
			id BIGINT PRIMARY KEY,
			parent_id BIGINT REFERENCES fkloader_test.tree(id) ON DELETE SET NULL ON UPDATE CASCADE
		)`,
		`CREATE TABLE fkloader_test_other.parent (a INT NOT NULL, b INT NOT NULL, PRIMARY KEY (a, b))`,
		`CREATE TABLE fkloader_test.child (
			a INT NOT NULL,
			b INT NOT NULL,
			CONSTRAINT child_parent_fkey FOREIGN KEY (a, b) REFERENCES fkloader_test_other.parent (a, b) ON DELETE RESTRICT
		)`,
	}
	for _, s := range stmts {
		if _, err := sess.Execute(ctx, models.Query{SQL: s}); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	t.Cleanup(func() {
		_, _ = sess.Execute(ctx, models.Query{SQL: `DROP SCHEMA IF EXISTS fkloader_test CASCADE`})
		_, _ = sess.Execute(ctx, models.Query{SQL: `DROP SCHEMA IF EXISTS fkloader_test_other CASCADE`})
	})

	// Self-referencing FK on fkloader_test.tree.
	treeFKs, err := sess.ListForeignKeys(ctx, "fkloader_test", "tree")
	if err != nil {
		t.Fatalf("ListForeignKeys tree: %v", err)
	}
	if len(treeFKs) != 1 {
		t.Fatalf("expected 1 self-FK on tree; got %+v", treeFKs)
	}
	tfk := treeFKs[0]
	if tfk.RefSchema != "fkloader_test" || tfk.RefTable != "tree" {
		t.Errorf("self-FK ref = %s.%s, want fkloader_test.tree", tfk.RefSchema, tfk.RefTable)
	}
	if !equalStrings(tfk.Columns, []string{"parent_id"}) {
		t.Errorf("self-FK Columns = %v", tfk.Columns)
	}
	if !equalStrings(tfk.RefColumns, []string{"id"}) {
		t.Errorf("self-FK RefColumns = %v", tfk.RefColumns)
	}
	if tfk.OnDelete != "SET NULL" {
		t.Errorf("self-FK OnDelete = %q, want SET NULL", tfk.OnDelete)
	}
	if tfk.OnUpdate != "CASCADE" {
		t.Errorf("self-FK OnUpdate = %q, want CASCADE", tfk.OnUpdate)
	}

	// Cross-schema + composite FK on fkloader_test.child.
	childFKs, err := sess.ListForeignKeys(ctx, "fkloader_test", "child")
	if err != nil {
		t.Fatalf("ListForeignKeys child: %v", err)
	}
	if len(childFKs) != 1 {
		t.Fatalf("expected 1 cross-schema composite FK on child; got %+v", childFKs)
	}
	cfk := childFKs[0]
	if cfk.RefSchema != "fkloader_test_other" || cfk.RefTable != "parent" {
		t.Errorf("composite FK ref = %s.%s, want fkloader_test_other.parent", cfk.RefSchema, cfk.RefTable)
	}
	if !equalStrings(cfk.Columns, []string{"a", "b"}) {
		t.Errorf("composite FK Columns = %v, want [a b] (matched-position order)", cfk.Columns)
	}
	if !equalStrings(cfk.RefColumns, []string{"a", "b"}) {
		t.Errorf("composite FK RefColumns = %v, want [a b]", cfk.RefColumns)
	}
	if cfk.OnDelete != "RESTRICT" {
		t.Errorf("composite FK OnDelete = %q, want RESTRICT", cfk.OnDelete)
	}
}

// TestFKReverse_InboundForFixtureUsers verifies that the inbound FK loader
// returns both fixture-defined references to app.users
// (app.posts.user_id and app.user_roles.user_id), both with ON DELETE
// CASCADE per the fixture schema. (B6).
func TestFKReverse_InboundForFixtureUsers(t *testing.T) {
	sess := openIntegrationSession(t)
	fks, err := sess.ListInboundForeignKeys(context.Background(), "app", "users")
	if err != nil {
		t.Fatalf("ListInboundForeignKeys: %v", err)
	}
	if len(fks) != 2 {
		t.Fatalf("len(fks) = %d, want 2 inbound FKs on app.users; got %+v", len(fks), fks)
	}
	byTable := map[string]models.ForeignKey{}
	for _, fk := range fks {
		byTable[fk.Table] = fk
	}
	posts, ok := byTable["posts"]
	if !ok {
		t.Fatalf("expected inbound FK from app.posts; got %+v", fks)
	}
	if posts.Schema != "app" || posts.Table != "posts" {
		t.Errorf("posts inbound FK Schema/Table = %s.%s, want app.posts", posts.Schema, posts.Table)
	}
	if posts.RefSchema != "app" || posts.RefTable != "users" {
		t.Errorf("posts inbound FK ref = %s.%s, want app.users", posts.RefSchema, posts.RefTable)
	}
	if !equalStrings(posts.Columns, []string{"user_id"}) {
		t.Errorf("posts inbound FK Columns = %v, want [user_id]", posts.Columns)
	}
	if !equalStrings(posts.RefColumns, []string{"id"}) {
		t.Errorf("posts inbound FK RefColumns = %v, want [id]", posts.RefColumns)
	}
	if posts.OnDelete != "CASCADE" {
		t.Errorf("posts inbound FK OnDelete = %q, want CASCADE", posts.OnDelete)
	}

	ur, ok := byTable["user_roles"]
	if !ok {
		t.Fatalf("expected inbound FK from app.user_roles; got %+v", fks)
	}
	if ur.OnDelete != "CASCADE" {
		t.Errorf("user_roles inbound FK OnDelete = %q, want CASCADE", ur.OnDelete)
	}
	if !equalStrings(ur.Columns, []string{"user_id"}) {
		t.Errorf("user_roles inbound FK Columns = %v, want [user_id]", ur.Columns)
	}
}

// TestFKReverse_NoInboundReturnsEmpty confirms the empty-case contract
// (non-nil empty slice). app.user_roles is a join table that no other
// fixture table references. (B6).
func TestFKReverse_NoInboundReturnsEmpty(t *testing.T) {
	sess := openIntegrationSession(t)
	fks, err := sess.ListInboundForeignKeys(context.Background(), "app", "user_roles")
	if err != nil {
		t.Fatalf("ListInboundForeignKeys: %v", err)
	}
	if len(fks) != 0 {
		t.Fatalf("expected 0 inbound FKs on app.user_roles, got %+v", fks)
	}
	if fks == nil {
		t.Fatalf("expected empty (non-nil) slice; got nil")
	}
}

// TestFKReverse_SelfAndCompositeCrossSchema mirrors the outbound loader's
// self-ref + composite + cross-schema coverage from the reverse side.
// (B6).
func TestFKReverse_SelfAndCompositeCrossSchema(t *testing.T) {
	sess := openIntegrationSession(t)
	ctx := context.Background()

	stmts := []string{
		`DROP SCHEMA IF EXISTS fkreverse_test CASCADE`,
		`DROP SCHEMA IF EXISTS fkreverse_test_other CASCADE`,
		`CREATE SCHEMA fkreverse_test`,
		`CREATE SCHEMA fkreverse_test_other`,
		`CREATE TABLE fkreverse_test.tree (
			id BIGINT PRIMARY KEY,
			parent_id BIGINT REFERENCES fkreverse_test.tree(id) ON DELETE SET NULL
		)`,
		`CREATE TABLE fkreverse_test_other.parent (a INT NOT NULL, b INT NOT NULL, PRIMARY KEY (a, b))`,
		`CREATE TABLE fkreverse_test.child (
			a INT NOT NULL,
			b INT NOT NULL,
			CONSTRAINT child_parent_fkey FOREIGN KEY (a, b) REFERENCES fkreverse_test_other.parent (a, b)
		)`,
	}
	for _, s := range stmts {
		if _, err := sess.Execute(ctx, models.Query{SQL: s}); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	t.Cleanup(func() {
		_, _ = sess.Execute(ctx, models.Query{SQL: `DROP SCHEMA IF EXISTS fkreverse_test CASCADE`})
		_, _ = sess.Execute(ctx, models.Query{SQL: `DROP SCHEMA IF EXISTS fkreverse_test_other CASCADE`})
	})

	// Self-ref: fkreverse_test.tree references itself, so it shows up as
	// its own inbound FK.
	treeIn, err := sess.ListInboundForeignKeys(ctx, "fkreverse_test", "tree")
	if err != nil {
		t.Fatalf("ListInboundForeignKeys tree: %v", err)
	}
	if len(treeIn) != 1 {
		t.Fatalf("expected 1 self-ref inbound FK on tree; got %+v", treeIn)
	}
	if treeIn[0].Schema != "fkreverse_test" || treeIn[0].Table != "tree" {
		t.Errorf("self inbound FK referrer = %s.%s, want fkreverse_test.tree",
			treeIn[0].Schema, treeIn[0].Table)
	}
	if treeIn[0].RefSchema != "fkreverse_test" || treeIn[0].RefTable != "tree" {
		t.Errorf("self inbound FK ref = %s.%s, want fkreverse_test.tree",
			treeIn[0].RefSchema, treeIn[0].RefTable)
	}

	// Cross-schema composite: parent in _other is referenced by child in
	// fkreverse_test.
	parentIn, err := sess.ListInboundForeignKeys(ctx, "fkreverse_test_other", "parent")
	if err != nil {
		t.Fatalf("ListInboundForeignKeys parent: %v", err)
	}
	if len(parentIn) != 1 {
		t.Fatalf("expected 1 inbound FK on parent; got %+v", parentIn)
	}
	if parentIn[0].Schema != "fkreverse_test" || parentIn[0].Table != "child" {
		t.Errorf("parent inbound FK referrer = %s.%s, want fkreverse_test.child",
			parentIn[0].Schema, parentIn[0].Table)
	}
	if !equalStrings(parentIn[0].Columns, []string{"a", "b"}) {
		t.Errorf("parent inbound FK Columns = %v, want [a b]", parentIn[0].Columns)
	}
	if !equalStrings(parentIn[0].RefColumns, []string{"a", "b"}) {
		t.Errorf("parent inbound FK RefColumns = %v, want [a b]", parentIn[0].RefColumns)
	}
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
