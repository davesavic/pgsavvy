package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/drivers/pg"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/pgsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/query"
	"github.com/jackc/pgx/v5"
	"github.com/spf13/afero"
)

func TestPendingEditRegistry_ForReturnsSameInstance(t *testing.T) {
	r := newPendingEditRegistry()
	a := r.For("conn1", models.Ref{Schema: "public", Table: "users"})
	b := r.For("conn1", models.Ref{Schema: "public", Table: "users"})
	if a == nil || a != b {
		t.Fatalf("For returned distinct instances for same key (a=%p b=%p)", a, b)
	}
}

func TestPendingEditRegistry_ForDistinctKeysDistinctSets(t *testing.T) {
	r := newPendingEditRegistry()
	a := r.For("conn1", models.Ref{Schema: "public", Table: "users"})
	bConn := r.For("conn2", models.Ref{Schema: "public", Table: "users"})
	bTable := r.For("conn1", models.Ref{Schema: "public", Table: "orders"})
	if a == nil || bConn == nil || bTable == nil {
		t.Fatalf("unexpected nil: a=%v bConn=%v bTable=%v", a, bConn, bTable)
	}
	if a == bConn || a == bTable || bConn == bTable {
		t.Fatalf("expected distinct sets for distinct keys")
	}
}

func TestPendingEditRegistry_ForEmptyKeyReturnsNil(t *testing.T) {
	r := newPendingEditRegistry()
	cases := []struct {
		conn string
		ref  models.Ref
	}{
		{"", models.Ref{Schema: "public", Table: "t"}},
		{"c", models.Ref{}},
		{"", models.Ref{}},
		{"c", models.Ref{Table: "t"}},
		{"c", models.Ref{Schema: "public"}},
	}
	for _, tc := range cases {
		if got := r.For(tc.conn, tc.ref); got != nil {
			t.Fatalf("For(%q,%+v) = %v, want nil", tc.conn, tc.ref, got)
		}
		if got := r.Lookup(tc.conn, tc.ref); got != nil {
			t.Fatalf("invalid Lookup = %v", got)
		}
	}
}

func TestPendingEditRegistry_LookupReturnsNilUntilFor(t *testing.T) {
	r := newPendingEditRegistry()
	ref := models.Ref{Schema: "public", Table: "users"}
	if got := r.Lookup("conn1", ref); got != nil {
		t.Fatalf("Lookup before For = %v, want nil", got)
	}
	_ = r.For("conn1", ref)
	if got := r.Lookup("conn1", ref); got == nil {
		t.Fatalf("Lookup after For = nil, want non-nil")
	}
}

func TestPendingEditRegistry_ForStampsTableRef(t *testing.T) {
	r := newPendingEditRegistry()
	s := r.For("conn1", models.Ref{Schema: "public", Table: "users"})
	want := models.Ref{Schema: "public", Table: "users"}
	if s.Table != want {
		t.Fatalf("Table = %+v, want %+v", s.Table, want)
	}
	bare := r.For("conn1", models.Ref{Schema: "public", Table: "users_only"})
	if bare.Table != (models.Ref{Schema: "public", Table: "users_only"}) {
		t.Fatalf("resolved Table = %+v, want public/users_only", bare.Table)
	}
}

func TestPendingEditSet_HasEdit(t *testing.T) {
	var s models.PendingEditSet
	pk := []any{42}
	if s.HasEdit(pk, "name") {
		t.Fatalf("HasEdit on empty set = true, want false")
	}
	if err := s.Add(models.PendingEdit{PrimaryKey: pk, Column: "name", NewValue: "bob", Kind: models.Literal}); err != nil {
		t.Fatalf("Add err = %v", err)
	}
	if !s.HasEdit(pk, "name") {
		t.Fatalf("HasEdit after Add = false, want true")
	}
	if s.HasEdit(pk, "other") {
		t.Fatalf("HasEdit on missing column = true, want false")
	}
	if s.HasEdit([]any{99}, "name") {
		t.Fatalf("HasEdit on missing pk = true, want false")
	}
}

func TestReview_target_registry(t *testing.T) {
	for _, scenario := range []string{"schemas", "qualified", "dotted"} {
		t.Run(scenario, func(t *testing.T) {
			tabs := ui.NewResultTabsHelper(ui.ResultTabsHelperDeps{})
			g := &Gui{resultTabsH: tabs, pendingEditReg: newPendingEditRegistry()}
			resolve := g.wireEditDeps().ActivePendingEditSet
			open := func(sql, schema, table string) *models.PendingEditSet {
				if err := tabs.OpenResultTab("target", nil); err != nil {
					t.Fatal(err)
				}
				tab := tabs.Active()
				tab.SetIdentity("conn", query.DetectFromQuery(sql))
				tab.Grid().SetColumns([]models.ColumnMeta{{Name: "id"}})
				tab.Grid().SetEditability(true, []int{0}, "", models.Ref{Schema: schema, Table: table})
				return resolve()
			}
			switch scenario {
			case "schemas":
				a := open("select id from t", "a", "t")
				a.Clear()
				b := open("select id from t", "b", "t")
				if a == b || b.Table != (models.Ref{Schema: "b", Table: "t"}) {
					t.Fatal("unqualified targets collided across schemas")
				}
			case "qualified":
				a := open("select id from t", "a", "t")
				b := open("select id from a.t", "a", "t")
				if a != b {
					t.Fatal("qualified and unqualified references split one relation's pending edits")
				}
			case "dotted":
				s := open(`select id from "t.u"`, "a.b", "t.u")
				if s == nil || s.Table != (models.Ref{Schema: "a.b", Table: "t.u"}) {
					t.Fatal("quoted dotted relation was split")
				}
			}
		})
	}
}

var reviewTargetDriverID atomic.Uint64

func TestReview_target_live(t *testing.T) {
	dsn := os.Getenv("PGSAVVY_TEST_PG")
	if dsn == "" {
		t.Skip("PGSAVVY_TEST_PG not set")
	}
	// Stream must provide authoritative relation metadata even when the
	// fixture uses pgx's normal cached execution mode.
	if _, err := pgx.ParseConfig(dsn); err != nil {
		t.Fatal("invalid isolated fixture configuration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("isolated fixture connection failed")
	}
	defer func() { _ = admin.Close(context.Background()) }()
	refs := []models.Ref{
		{Schema: "review.target.a", Table: "t"},
		{Schema: "review.target.b", Table: "t"},
		{Schema: "review.target.a", Table: "t.u"},
		{Schema: "review.target.b", Table: `t."u`},
	}
	for _, schema := range []string{"review.target.a", "review.target.b"} {
		if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pg.QuoteIdent(schema)); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cleanupCancel()
			cleanupConn, cleanupErr := pgx.Connect(cleanupCtx, dsn)
			if cleanupErr != nil {
				t.Error("fixture cleanup connection failed")
				return
			}
			defer func() { _ = cleanupConn.Close(cleanupCtx) }()
			if _, err := cleanupConn.Exec(cleanupCtx, "DROP SCHEMA "+pg.QuoteIdent(schema)+" CASCADE"); err != nil {
				t.Error(err)
			}
		})
	}
	for _, ref := range refs {
		if _, err := admin.Exec(ctx, "CREATE TABLE "+pg.QuoteQualified(ref.Schema, ref.Table)+" (id integer PRIMARY KEY, value text); INSERT INTO "+pg.QuoteQualified(ref.Schema, ref.Table)+" VALUES (1, 'old'), (2, 'untouched')"); err != nil {
			t.Fatal(err)
		}
	}

	fs := afero.NewMemMapFs()
	c := common.NewCommon(slog.New(slog.DiscardHandler), i18n.EnglishTranslationSet(), config.GetDefaultConfig(), &common.AppState{}, fs)
	tmp := t.TempDir()
	driverName := fmt.Sprintf("review-target-live-%d", reviewTargetDriverID.Add(1))
	g := NewGui(Deps{
		Common: c, Store: common.NewAppStateStore(fs, "/state.yml", common.DefaultClock()),
		HistoryProvider:     func() (*query.History, error) { return query.New(filepath.Join(tmp, "history.sqlite")) },
		ConnectionsProvider: func() []models.Connection { return nil }, DriverNamesFn: func() []string { return []string{driverName} },
		ConnectionsPath: "/connections.yml",
	})
	if err := g.UseDriverForTest(testfake.NewRecorderGuiDriver()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := g.Close(); err != nil {
			t.Error(err)
		}
	})
	drivers.Register(driverName, pg.New(nil))
	profile := &models.Connection{Name: "target", Driver: driverName, DSN: dsn}
	if err := g.HelperBagForTest().Connect.Connect(ctx, profile); err != nil {
		t.Fatal("live GUI connect failed")
	}
	conn := g.connectHelper.Connection()
	resolve := g.wireEditDeps().ActivePendingEditSet
	open := func(sql, schema string) *models.PendingEditSet {
		t.Helper()
		if err := g.resultTabsH.CloseActive(); err != nil {
			t.Fatal(err)
		}
		sess, err := conn.AcquireSession(ctx)
		if err != nil {
			t.Fatal(err)
		}
		rs, err := sess.Stream(ctx, models.Query{SQL: sql, DefaultSchema: schema})
		if err != nil {
			_ = sess.Close()
			t.Fatal(err)
		}
		cols := rs.Columns()
		if err := rs.Close(); err != nil {
			t.Fatal(err)
		}
		if err := sess.Close(); err != nil {
			t.Fatal(err)
		}
		if err := g.resultTabsH.OpenResultTab("target", nil); err != nil {
			t.Fatal(err)
		}
		g.resultTabsH.Active().Grid().SetColumns(cols)
		g.resultTabsH.AttachActiveTabIdentity("target", query.DetectFromQuery(sql))
		if err := g.spinnerState.workersWG.WaitTimeout(5 * time.Second); err != nil {
			t.Fatal(err)
		}
		return resolve()
	}
	apply := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: conn})
	stageAndApply := func(set *models.PendingEditSet, value string) {
		t.Helper()
		if set == nil {
			t.Fatal("catalog-resolved edit target missing")
		}
		if err := set.BindIdentity(g.resultTabsH.Active().Grid().EditIdentity()); err != nil {
			t.Fatal(err)
		}
		if err := set.Add(models.PendingEdit{PrimaryKey: []any{int32(1)}, Column: "value", OldValue: "old", NewValue: value, Kind: models.Literal}); err != nil {
			t.Fatal(err)
		}
		_, conflicts, err := apply.Apply(ctx, set, []string{"id"}, false)
		if err != nil || len(conflicts) != 0 {
			t.Fatalf("apply err=%v conflicts=%d", err, len(conflicts))
		}
		set.Clear()
	}

	a := open("select id, value from t", refs[0].Schema)
	if a == nil {
		t.Fatal("first target missing")
	}
	a.Clear()
	b := open("select id, value from t", refs[1].Schema)
	if b == nil || b == a || b.Table != refs[1] {
		t.Fatalf("live unqualified targets collided: a=%p b=%p target=%+v editable=%v reason=%q", a, b, g.resultTabsH.Active().Grid().IdentityRef(), g.resultTabsH.Active().Grid().Editable(), g.resultTabsH.Active().Grid().DisabledReason())
	}
	stageAndApply(b, "b-edited")
	if qualified := open("select id, value from "+pg.QuoteQualified(refs[1].Schema, refs[1].Table), ""); qualified != b {
		t.Fatal("live qualified reference split pending ownership")
	}
	for i, ref := range refs[2:] {
		set := open("select id, value from "+pg.QuoteIdent(ref.Table), ref.Schema)
		if set == nil || set.Table != ref {
			t.Fatal("live quoted identifier was flattened")
		}
		stageAndApply(set, "dotted-edited")
		if i == 0 {
			if qualified := open("select id, value from "+pg.QuoteQualified(ref.Schema, ref.Table), ""); qualified != set {
				t.Fatal("dotted qualified reference split ownership")
			}
		}
	}
	for i, ref := range refs {
		want := "old"
		if i == 1 {
			want = "b-edited"
		}
		if i >= 2 {
			want = "dotted-edited"
		}
		for id, expected := range map[int]string{1: want, 2: "untouched"} {
			var got string
			if err := admin.QueryRow(ctx, "select value from "+pg.QuoteQualified(ref.Schema, ref.Table)+" where id=$1", id).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != expected {
				t.Fatalf("relation %+v row %d=%q, want %q", ref, id, got, expected)
			}
		}
	}
	profile.ReadOnly = true
	if err := g.retireConnection(nil); err != nil {
		t.Fatal(err)
	}
	if err := g.HelperBagForTest().Connect.Connect(ctx, profile); err != nil {
		t.Fatalf("read-only live reconnect failed: %v", err)
	}
	conn = g.connectHelper.Connection()
	if got := open("select id, value from t", refs[0].Schema); got != nil {
		t.Fatal("read-only connection authorized staging")
	}
	if got := g.resultTabsH.Active().Grid().DisabledReason(); got != "read-only connection" {
		t.Fatalf("disabled reason = %q", got)
	}
}
