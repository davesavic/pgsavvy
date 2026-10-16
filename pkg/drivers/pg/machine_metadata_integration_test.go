//go:build integration && linux

package pg

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/davesavic/pgsavvy/pkg/models"
)

type metadataLiveFixture struct {
	admin   *pgx.Conn
	profile models.Connection
	schema  string
	schemas []string
}

func metadataLive(t *testing.T) *metadataLiveFixture {
	t.Helper()
	dsn := os.Getenv("PGSAVVY_TEST_PG")
	if dsn == "" {
		t.Fatal("metadata integration requires designated PGSAVVY_TEST_PG; missing fixtures do not skip")
	}
	machineTestEnvironment(t)
	admin, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal("metadata fixture unreachable")
	}
	var version int
	if admin.QueryRow(context.Background(), "SELECT version FROM app._fixture_meta").Scan(&version) != nil || version != 2 {
		_ = admin.Close(context.Background())
		t.Fatal("metadata requires designated seeded fixture")
	}
	name := "first_meta_" + strings.ToLower(rand.Text())
	role := name + "_reader"
	quotedRole := pgx.Identifier{role}.Sanitize()
	fixture := &metadataLiveFixture{admin: admin, schema: name}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for i := len(fixture.schemas) - 1; i >= 0; i-- {
			if _, err := admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+pgx.Identifier{fixture.schemas[i]}.Sanitize()+" CASCADE"); err != nil {
				t.Error("test-owned schema teardown failed")
			}
		}
		if _, err := admin.Exec(ctx, "DROP ROLE IF EXISTS "+quotedRole); err != nil {
			t.Error("test-owned role teardown failed")
		}
		_ = admin.Close(ctx)
	})
	fixture.exec(t, "CREATE ROLE "+quotedRole+" LOGIN PASSWORD 'v17a_synthetic_password'")
	fixture.newSchema(t, name)
	fixture.exec(t, "GRANT USAGE ON SCHEMA "+pgx.Identifier{name}.Sanitize()+" TO "+quotedRole)
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme == "" {
		t.Fatal("metadata fixture needs PostgreSQL URL")
	}
	u.User = url.UserPassword(role, "v17a_synthetic_password")
	fixture.profile = models.Connection{Name: "metadata-fixture", Driver: "postgres", DSN: u.String(), CLIAccess: models.CLIAccessMetadata}
	return fixture
}

func (f *metadataLiveFixture) exec(t *testing.T, sql string) {
	t.Helper()
	if _, err := f.admin.Exec(context.Background(), sql); err != nil {
		t.Fatalf("test-owned metadata DDL failed (%T)", err)
	}
}
func (f *metadataLiveFixture) newSchema(t *testing.T, name string) {
	t.Helper()
	f.exec(t, "CREATE SCHEMA "+pgx.Identifier{name}.Sanitize())
	f.schemas = append(f.schemas, name)
}
func (f *metadataLiveFixture) inspect(t *testing.T, request MachineMetadataRequest) (MachineMetadata, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := OpenMachine(ctx, f.profile, MachineOptions{RequiredAccess: models.CLIAccessMetadata})
	if err != nil {
		return MachineMetadata{}, err
	}
	result, err := client.Inspect(ctx, request)
	cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
	closeErr := client.Close(cleanup)
	stop()
	if err == nil {
		err = closeErr
	}
	return result, err
}

func TestMachineMetadata_Paging_Live(t *testing.T) {
	f := metadataLive(t)
	for _, suffix := range []string{"a_hidden", "b_hidden", "c_visible"} {
		f.newSchema(t, f.schema+suffix)
	}
	page, err := f.inspect(t, MachineMetadataRequest{Operation: MachineSchemas, After: f.schema, Limit: 2})
	if err != nil || len(page.Schemas.Items) != 2 || page.Schemas.RawComplete || page.Schemas.NextAfter == nil || *page.Schemas.NextAfter != f.schema+"b_hidden" {
		t.Fatalf("raw schema page: %+v %v", page.Schemas, err)
	}
	page, err = f.inspect(t, MachineMetadataRequest{Operation: MachineSchemas, After: *page.Schemas.NextAfter, Limit: 1})
	if err != nil || page.Schemas.Items[0].Name != f.schema+"c_visible" {
		t.Fatal("visible lookahead skipped after all-hidden raw prefix")
	}
	for _, name := range []string{"a.hidden", "b.hidden", "c.visible"} {
		f.exec(t, "CREATE TABLE "+pgx.Identifier{f.schema, name}.Sanitize()+" (id integer)")
	}
	page, err = f.inspect(t, MachineMetadataRequest{Operation: MachineTables, Schema: f.schema, Limit: 2})
	if err != nil || len(page.Tables.Items) != 2 || *page.Tables.NextAfter != "b.hidden" || page.Tables.RawComplete {
		t.Fatalf("table page: %+v %v", page.Tables, err)
	}
	page, err = f.inspect(t, MachineMetadataRequest{Operation: MachineTables, Schema: f.schema, After: *page.Tables.NextAfter, Limit: 2})
	if err != nil || len(page.Tables.Items) != 1 || page.Tables.Items[0].Name != "c.visible" || !page.Tables.RawComplete {
		t.Fatal("table continuation not exact")
	}
	empty := f.schema + ".Empty"
	f.newSchema(t, empty)
	page, err = f.inspect(t, MachineMetadataRequest{Operation: MachineTables, Schema: empty})
	if err != nil || page.Tables.Items == nil || len(page.Tables.Items) != 0 || !page.Tables.RawComplete {
		t.Fatal("empty existing schema not complete empty array")
	}
}

func TestMachineMetadata_Describe_Live(t *testing.T) {
	f := metadataLive(t)
	schema := pgx.Identifier{f.schema}.Sanitize()
	table := pgx.Identifier{f.schema, "Thing.With.Dot"}.Sanitize()
	f.exec(t, "CREATE DOMAIN "+schema+".not_null_text AS text NOT NULL")
	f.exec(t, "CREATE TABLE "+table+" (id integer NOT NULL PRIMARY KEY, name text DEFAULT 'untitled', free text, required "+schema+".not_null_text, generated text GENERATED ALWAYS AS (name || '!') STORED)")
	f.exec(t, "COMMENT ON COLUMN "+table+".name IS 'untrusted SQL: DELETE <tag> & comments'")
	result, err := f.inspect(t, MachineMetadataRequest{Operation: MachineDescribe, Schema: f.schema, Table: "Thing.With.Dot"})
	if err != nil {
		t.Fatalf("describe live: %v", err)
	}
	description := result.Description
	if description.Relation.Name != "Thing.With.Dot" || description.Relation.Comment != nil || len(description.Columns) != 5 {
		t.Fatal("exact relation/column shape lost")
	}
	columns := map[string]MachineCatalogColumn{}
	for _, column := range description.Columns {
		columns[column.Name] = column
	}
	if *columns["id"].Nullable || !*columns["free"].Nullable || *columns["required"].Nullable || columns["free"].Default != nil || columns["free"].Comment != nil || columns["generated"].Default != nil || columns["name"].Default == nil || !strings.Contains(*columns["name"].Default, "untitled") || columns["name"].Comment == nil || *columns["name"].Comment != "untrusted SQL: DELETE <tag> & comments" {
		t.Fatal("nullability/default/comment facts changed")
	}
	primary := false
	for _, constraint := range description.Constraints {
		if constraint.Kind == "p" && len(constraint.Columns) == 1 && constraint.Columns[0] == "id" {
			primary = true
		}
	}
	if !primary {
		t.Fatal("primary-key facts missing")
	}
	for _, sql := range []string{"CREATE VIEW " + schema + ".a_view AS SELECT 1 AS x", "CREATE MATERIALIZED VIEW " + schema + ".a_materialized AS SELECT 1 AS x", "CREATE TABLE " + schema + ".a_partitioned (id integer) PARTITION BY RANGE (id)", "CREATE SEQUENCE " + schema + ".excluded_sequence"} {
		f.exec(t, sql)
	}
	page, err := f.inspect(t, MachineMetadataRequest{Operation: MachineTables, Schema: f.schema})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, item := range page.Tables.Items {
		kinds[item.Kind] = true
		if item.Name == "excluded_sequence" {
			t.Fatal("kind support broadened")
		}
	}
	for _, kind := range []string{"table", "view", "materialized_view", "partitioned_table"} {
		if !kinds[kind] {
			t.Fatal("supported relation kind omitted")
		}
	}
	if _, err := f.inspect(t, MachineMetadataRequest{Operation: MachineDescribe, Schema: f.schema, Table: "excluded_sequence"}); err == nil {
		t.Fatal("unsupported kind described")
	}
}

func TestMachineMetadata_Indexes_Live(t *testing.T) {
	f := metadataLive(t)
	table := pgx.Identifier{f.schema, "Indexed.Table"}.Sanitize()
	index := pgx.Identifier{f.schema, "Expr.With.Dot"}.Sanitize()
	f.exec(t, "CREATE TABLE "+table+" (id integer PRIMARY KEY, name text, extra text)")
	f.exec(t, "CREATE UNIQUE INDEX "+pgx.Identifier{"Expr.With.Dot"}.Sanitize()+" ON "+table+" ((lower(name))) INCLUDE (extra) WHERE id > 0")
	result, err := f.inspect(t, MachineMetadataRequest{Operation: MachineDescribe, Schema: f.schema, Table: "Indexed.Table"})
	if err != nil {
		t.Fatal(err)
	}
	var expected string
	if f.admin.QueryRow(context.Background(), "SELECT pg_catalog.pg_get_indexdef($1::regclass)", index).Scan(&expected) != nil {
		t.Fatal("index fixture lookup failed")
	}
	found := false
	for _, item := range result.Description.Indexes {
		if item.Name == "Expr.With.Dot" {
			found = true
			if item.Definition != expected || !item.Unique || item.Primary || item.Method != "btree" || !strings.Contains(item.Definition, "INCLUDE") || !strings.Contains(item.Definition, "WHERE") {
				t.Fatal("expression/include/predicate index semantics lost")
			}
		}
	}
	if !found {
		t.Fatal("expression index missing")
	}
}

func TestMachineMetadata_Relationships_Live(t *testing.T) {
	f := metadataLive(t)
	other := f.schema + ".Other"
	f.newSchema(t, other)
	parent := pgx.Identifier{f.schema, "Parent.With.Dot"}.Sanitize()
	child := pgx.Identifier{other, "Child.With.Dot"}.Sanitize()
	self := pgx.Identifier{f.schema, "Self"}.Sanitize()
	f.exec(t, "CREATE TABLE "+parent+" (a integer, b integer, PRIMARY KEY(a,b), UNIQUE(b,a))")
	f.exec(t, "CREATE TABLE "+child+" (x integer, y integer, FOREIGN KEY(y,x) REFERENCES "+parent+"(b,a) ON UPDATE SET NULL ON DELETE CASCADE)")
	f.exec(t, "CREATE TABLE "+self+" (id integer PRIMARY KEY, parent integer REFERENCES "+self+"(id))")
	result, err := f.inspect(t, MachineMetadataRequest{Operation: MachineRelationships, Schema: other, Table: "Child.With.Dot"})
	if err != nil || len(result.Relationships.Outbound) != 1 {
		t.Fatalf("outbound relationships: %v", err)
	}
	key := result.Relationships.Outbound[0]
	if key.Schema != other || key.Table != "Child.With.Dot" || key.RefSchema != f.schema || key.RefTable != "Parent.With.Dot" || strings.Join(key.Columns, ",") != "y,x" || strings.Join(key.RefColumns, ",") != "b,a" || key.OnUpdate != "SET NULL" || key.OnDelete != "CASCADE" {
		t.Fatal("composite cross-schema pairing changed")
	}
	result, err = f.inspect(t, MachineMetadataRequest{Operation: MachineRelationships, Schema: f.schema, Table: "Parent.With.Dot"})
	if err != nil || len(result.Relationships.Inbound) != 1 || len(result.Relationships.Outbound) != 0 {
		t.Fatal("inbound direction lost")
	}
	result, err = f.inspect(t, MachineMetadataRequest{Operation: MachineRelationships, Schema: f.schema, Table: "Self"})
	if err != nil || len(result.Relationships.Inbound) != 1 || len(result.Relationships.Outbound) != 1 {
		t.Fatal("self reference missing from a direction")
	}
}

func TestMachineMetadata_Failures_Live(t *testing.T) {
	f := metadataLive(t)
	table := pgx.Identifier{f.schema, "target"}.Sanitize()
	f.exec(t, "CREATE TABLE "+table+" (x integer)")
	for _, request := range []MachineMetadataRequest{{Operation: MachineDescribe, Schema: f.schema, Table: "missing"}, {Operation: MachineRelationships, Schema: f.schema, Table: "missing"}, {Operation: MachineTables, Schema: f.schema + "missing"}} {
		_, err := f.inspect(t, request)
		var safe *MachineError
		if !errors.As(err, &safe) || safe.Code != "OBJECT_NOT_FOUND" {
			t.Fatal("missing object became empty metadata")
		}
	}
	long := strings.Repeat("x", 63)
	f.exec(t, "CREATE TABLE "+pgx.Identifier{f.schema, long}.Sanitize()+" (x integer)")
	if _, err := f.inspect(t, MachineMetadataRequest{Operation: MachineDescribe, Schema: f.schema, Table: long + "extra"}); err == nil {
		t.Fatal("name parameter silently truncated")
	}
	comment := strings.Repeat("x", 65537)
	f.exec(t, "COMMENT ON TABLE "+table+" IS '"+comment+"'")
	if _, err := f.inspect(t, MachineMetadataRequest{Operation: MachineDescribe, Schema: f.schema, Table: "target"}); err == nil {
		t.Fatal("oversized metadata accepted")
	} else {
		var safe *MachineError
		if !errors.As(err, &safe) || safe.Code != "RESOURCE_FAILURE" {
			t.Fatal("oversized catalog value misclassified")
		}
	}
	f.exec(t, "DROP TABLE "+table)
	if _, err := f.inspect(t, MachineMetadataRequest{Operation: MachineDescribe, Schema: f.schema, Table: "target"}); err == nil {
		t.Fatal("dropped object described")
	}
	columns := make([]string, 1000)
	for i := range columns {
		columns[i] = fmt.Sprintf("c%d text", i)
	}
	f.exec(t, "CREATE TABLE "+pgx.Identifier{f.schema, "capped"}.Sanitize()+" ("+strings.Join(columns, ",")+")")
	result, err := f.inspect(t, MachineMetadataRequest{Operation: MachineDescribe, Schema: f.schema, Table: "capped"})
	if err != nil || len(result.Description.Columns) != 1000 {
		t.Fatalf("exact aggregate cap failed: %v", err)
	}
	f.exec(t, "ALTER TABLE "+pgx.Identifier{f.schema, "capped"}.Sanitize()+" ADD COLUMN overflow text")
	result, err = f.inspect(t, MachineMetadataRequest{Operation: MachineDescribe, Schema: f.schema, Table: "capped"})
	var safe *MachineError
	if !errors.As(err, &safe) || safe.Code != "RESOURCE_FAILURE" || result.Description != nil {
		t.Fatal("aggregate overflow became complete partial facts")
	}
}

func TestMachineMetadata_Failures_ConcurrentDDL_Live(t *testing.T) {
	f := metadataLive(t)
	table := pgx.Identifier{f.schema, "changing"}.Sanitize()
	f.exec(t, "CREATE TABLE "+table+" (before_a text, before_b text)")
	change, done := make(chan struct{}), make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	joined := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		select {
		case <-joined:
		case <-time.After(time.Second):
			t.Error("concurrent DDL fixture did not join")
		}
	})
	go func() {
		defer close(joined)
		for i := 0; i < 10; i++ {
			select {
			case <-ctx.Done():
				return
			case <-change:
			}
			from, to := "before", "after"
			if i%2 == 1 {
				from, to = to, from
			}
			tx, err := f.admin.Begin(ctx)
			if err != nil {
				done <- err
				return
			}
			_, err = tx.Exec(ctx, "ALTER TABLE "+table+" RENAME COLUMN "+from+"_a TO "+to+"_a; ALTER TABLE "+table+" RENAME COLUMN "+from+"_b TO "+to+"_b")
			if err == nil {
				err = tx.Commit(ctx)
			} else {
				_ = tx.Rollback(context.Background())
			}
			if err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	success := 0
	for range 10 {
		select {
		case change <- struct{}{}:
		case err := <-done:
			t.Fatalf("concurrent DDL stopped early (%T)", err)
		}
		result, err := f.inspect(t, MachineMetadataRequest{Operation: MachineDescribe, Schema: f.schema, Table: "changing"})
		if err != nil {
			continue
		}
		success++
		columns := result.Description.Columns
		if len(columns) != 2 || !((columns[0].Name == "before_a" && columns[1].Name == "before_b") || (columns[0].Name == "after_a" && columns[1].Name == "after_b")) {
			t.Fatal("composed catalog snapshot mixed DDL states")
		}
	}
	if err := <-done; err != nil {
		t.Fatal("concurrent DDL failed")
	}
	if success == 0 {
		t.Fatal("concurrent DDL produced no inspectable snapshot")
	}
}
