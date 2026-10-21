//go:build integration && (linux || darwin)

package pg

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("metadata fixture unreachable")
	}
	var version int
	if admin.QueryRow(ctx, "SELECT version FROM app._fixture_meta").Scan(&version) != nil || version != 2 {
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
		_ = admin.Close(cleanup)
		stop()
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := f.admin.Exec(ctx, sql); err != nil {
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

func TestMachineMetadata_TargetSelection_Live(t *testing.T) {
	f := metadataLive(t)
	base, err := url.Parse(f.profile.DSN)
	if err != nil {
		t.Fatal("fixture URL invalid")
	}
	q := base.Query()
	q.Set("sslmode", "disable")
	q.Del("target_session_attrs")
	base.RawQuery = q.Encode()
	baseline, err := pgx.ParseConfig(base.String())
	if err != nil {
		t.Fatal("fixture configuration invalid")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	direct, err := pgx.ConnectConfig(ctx, baseline)
	if err != nil {
		t.Fatal("fixture role baseline connection failed")
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		if err := direct.Close(cleanup); err != nil {
			t.Error("fixture baseline cleanup failed")
		}
	})
	var readOnly string
	var recovery bool
	if err := direct.QueryRow(ctx, "SELECT current_setting('transaction_read_only'), pg_is_in_recovery()").Scan(&readOnly, &recovery); err != nil || readOnly != "off" || recovery {
		t.Fatal("fixture role must start writable on a primary")
	}
	quote := func(s string) string {
		return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
	}
	keyword := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable target_session_attrs=read-write", quote(baseline.Host), baseline.Port, quote(baseline.User), quote(baseline.Password), quote(baseline.Database))
	fixtureIP, _, err := net.SplitHostPort(direct.PgConn().Conn().RemoteAddr().String())
	if err != nil {
		t.Fatal("fixture must use TCP")
	}
	for _, tc := range []struct {
		name, selector, startup, env, options, envOptions string
		keyword, reject, fallback, prefer                 bool
		multipleAddresses                                 bool
	}{
		{name: "url_read_write", selector: "read-write"},
		{name: "keyword_read_write", keyword: true},
		{name: "environment_read_write", env: "read-write"},
		{name: "explicit_read_write_over_environment", selector: "read-write", env: "read-only"},
		{name: "explicit_any_over_environment", selector: "any", env: "read-write", startup: "on"},
		{name: "read_only_rejects_primary", selector: "read-only", reject: true},
		{name: "read_only_rejects_all_addresses", selector: "read-only", reject: true, multipleAddresses: true},
		{name: "startup_on_rejects_read_write", selector: "read-write", startup: "on", reject: true},
		{name: "startup_on_accepts_read_only", selector: "read-only", startup: "on"},
		{name: "startup_off_accepts_read_write", selector: "read-write", startup: "off"},
		{name: "pgoptions_on_rejects_read_write", selector: "read-write", envOptions: "-c default_transaction_read_only=on", reject: true},
		{name: "explicit_options_override_environment", selector: "read-write", envOptions: "-c default_transaction_read_only=on", options: "-c default_transaction_read_only=off"},
		{name: "ordered_endpoint_fallback", selector: "read-write", fallback: true},
		{name: "prefer_standby_reconnect", selector: "prefer-standby", prefer: true},
		{name: "prefer_standby_all_addresses", selector: "prefer-standby", prefer: true, multipleAddresses: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PGTARGETSESSIONATTRS", tc.env)
			t.Setenv("PGOPTIONS", tc.envOptions)
			u := *base
			params := u.Query()
			if tc.selector != "" {
				params.Set("target_session_attrs", tc.selector)
			}
			if tc.startup != "" {
				params.Set("default_transaction_read_only", tc.startup)
			}
			if tc.options != "" {
				params.Set("options", tc.options)
			}
			if tc.fallback {
				u.Host = "127.0.0.2:5432,127.0.0.3:5432"
			}
			u.RawQuery = params.Encode()
			profile := f.profile
			profile.DSN = u.String()
			if tc.keyword {
				profile.DSN = keyword
			}
			var validated []*pgconn.PgConn
			var validationErrors []error
			var startupStates []string
			options := MachineOptions{ParseConfig: func(ctx context.Context, p models.Connection) (*pgx.ConnConfig, error) {
				cfg, err := session.BuildMachineConfig(ctx, p)
				if err != nil {
					return nil, err
				}
				if tc.multipleAddresses {
					// Two reachable candidates make address fallback deterministic,
					// including on hosts without both IPv4 and IPv6 loopback.
					cfg.LookupFunc = func(context.Context, string) ([]string, error) {
						return []string{fixtureIP, fixtureIP}, nil
					}
				}
				validate := cfg.ValidateConnect
				if tc.selector != "any" && validate == nil {
					t.Fatal("original target validator missing")
				}
				if validate != nil {
					cfg.ValidateConnect = func(ctx context.Context, conn *pgconn.PgConn) error {
						validated = append(validated, conn)
						results, err := conn.Exec(ctx, "SHOW transaction_read_only").ReadAll()
						if err != nil || len(results) != 1 || len(results[0].Rows) != 1 {
							t.Fatal("startup state lookup failed")
						}
						startupStates = append(startupStates, string(results[0].Rows[0][0]))
						err = validate(ctx, conn)
						validationErrors = append(validationErrors, err)
						return err
					}
				}
				return cfg, nil
			}}
			var dials []string
			if tc.fallback {
				options.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
					dials = append(dials, network+" "+addr)
					switch network + " " + addr {
					case "tcp 127.0.0.2:5432":
						return nil, errors.New("test-owned endpoint refusal")
					case "tcp 127.0.0.3:5432":
						return (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(baseline.Host, fmt.Sprint(baseline.Port)))
					default:
						t.Errorf("unexpected endpoint: %s %s", network, addr)
						return nil, errors.New("unexpected endpoint")
					}
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			t.Cleanup(cancel)
			client, err := OpenMachine(ctx, profile, options)
			if client != nil {
				t.Cleanup(func() {
					cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
					defer stop()
					if err := client.Close(cleanup); err != nil {
						t.Error("selected client cleanup failed")
					}
				})
			}
			if tc.reject {
				var safe *MachineError
				if client != nil || !errors.As(err, &safe) || safe.Code != "CONNECTION_UNAVAILABLE" || len(validationErrors) == 0 {
					t.Fatalf("original selector rejection lost: %v", err)
				}
				if tc.multipleAddresses && len(validationErrors) != 2 {
					t.Fatalf("expected both resolved candidates to be rejected, got %d", len(validationErrors))
				}
				for _, validationErr := range validationErrors {
					if validationErr == nil {
						t.Fatal("original selector accepted a rejected candidate")
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("target selection failed: %v", err)
			}
			if tc.selector != "any" {
				wantStartup := "off"
				if tc.startup == "on" {
					wantStartup = "on"
				}
				if len(startupStates) == 0 || startupStates[0] != wantStartup {
					t.Fatalf("startup state before original validation: %v want %s", startupStates, wantStartup)
				}
			}
			if tc.fallback && !reflect.DeepEqual(dials, []string{"tcp 127.0.0.2:5432", "tcp 127.0.0.3:5432"}) {
				t.Fatalf("endpoint fallback order: %v", dials)
			}
			if tc.prefer {
				if len(validated) < 2 || client.conn.PgConn() != validated[len(validated)-1] {
					t.Fatal("prefer-standby did not reconnect after validating all candidates")
				}
				if tc.multipleAddresses && len(validated) != 3 {
					t.Fatalf("expected two candidates and one reconnect, got %d", len(validated))
				}
				pids := make(map[uint32]bool)
				for _, conn := range validated {
					if pids[conn.PID()] {
						t.Fatal("prefer-standby reused a rejected connection")
					}
					pids[conn.PID()] = true
				}
				for _, err := range validationErrors {
					if _, ok := err.(*pgconn.NotPreferredError); !ok {
						t.Fatal("original nonpreferred result lost")
					}
				}
				for _, conn := range validated[:len(validated)-1] {
					select {
					case <-conn.CleanupDone():
					default:
						t.Fatal("rejected connection cleanup not joined")
					}
					if !conn.IsClosed() {
						t.Fatal("rejected connection remains open")
					}
				}
			}
			var sessionReadOnly string
			if err := client.conn.QueryRow(ctx, "SHOW default_transaction_read_only").Scan(&sessionReadOnly); err != nil || sessionReadOnly != "on" {
				t.Fatal("post-selection session is not read-only")
			}
			for _, repeatable := range []bool{false, true} {
				if err := client.withReadOnly(ctx, repeatable, "", func(ctx context.Context) error {
					var operationReadOnly string
					if err := client.conn.QueryRow(ctx, "SHOW transaction_read_only").Scan(&operationReadOnly); err != nil {
						return err
					}
					if operationReadOnly != "on" {
						return errors.New("operation is not read-only")
					}
					return nil
				}); err != nil {
					t.Fatalf("read-only operation: %v", err)
				}
				if client.conn.PgConn().TxStatus() != 'I' {
					t.Fatal("operation did not roll back")
				}
			}
		})
	}
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
		if len(columns) != 2 || (columns[0].Name != "before_a" || columns[1].Name != "before_b") && (columns[0].Name != "after_a" || columns[1].Name != "after_b") {
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
