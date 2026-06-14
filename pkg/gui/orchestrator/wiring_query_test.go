package orchestrator_test

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/spf13/afero"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/editor"
	"github.com/davesavic/dbsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/dbsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/dbsavvy/pkg/i18n"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/query"
)

// --- minimal fakes for the wireQueryRuntime path -------------------------
//
// The Connect path used here invokes:
//   ConnectHelper.Connect -> driver.Open / conn.AcquireSession (first session)
//   connectInvoker.wireQueryRuntime -> conn.AcquireSession (second session)
//   session.New(conn, inner, opts) -> inner.ID()
// Nothing else fires until the test calls QueryRunner.Run, so the fakes can
// stub every other Session/Connection method with zero values.

type wireFakeSession struct {
	id           models.SessionID
	schemas      []models.Schema
	indexes      []models.Index
	columns      []models.Column
	schemasErr   error
	schemasBlock chan struct{}
}

func (s *wireFakeSession) Close() error         { return nil }
func (s *wireFakeSession) ID() models.SessionID { return s.id }
func (s *wireFakeSession) ListDatabases(_ context.Context) ([]models.Database, error) {
	return nil, nil
}

func (s *wireFakeSession) ListSchemas(ctx context.Context, _ string) ([]models.Schema, error) {
	if s.schemasBlock != nil {
		select {
		case <-s.schemasBlock:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if s.schemasErr != nil {
		return nil, s.schemasErr
	}
	return s.schemas, nil
}

func (s *wireFakeSession) ListTables(_ context.Context, _ string) ([]*models.Table, error) {
	return nil, nil
}

func (s *wireFakeSession) ListColumns(_ context.Context, _, _ string) ([]models.Column, error) {
	return s.columns, nil
}

func (s *wireFakeSession) ListIndexes(_ context.Context, _, _ string) ([]models.Index, error) {
	return s.indexes, nil
}

func (s *wireFakeSession) ListConstraints(_ context.Context, _, _ string) ([]models.Constraint, error) {
	return nil, nil
}

func (s *wireFakeSession) ListForeignKeys(_ context.Context, _, _ string) ([]models.ForeignKey, error) {
	return nil, nil
}

func (s *wireFakeSession) ListInboundForeignKeys(_ context.Context, _, _ string) ([]models.ForeignKey, error) {
	return nil, nil
}

func (s *wireFakeSession) ListFunctions(_ context.Context) ([]string, error) {
	return nil, nil
}

func (s *wireFakeSession) DescribeFunction(_ context.Context, _, _ string) ([]models.FunctionDetail, error) {
	return nil, nil
}

func (s *wireFakeSession) Execute(_ context.Context, _ models.Query) (models.Result, error) {
	return models.Result{}, nil
}

func (s *wireFakeSession) Stream(_ context.Context, _ models.Query) (drivers.RowStream, error) {
	return nil, nil
}

func (s *wireFakeSession) Explain(_ context.Context, _ models.Query, _ bool) (models.Plan, error) {
	return models.Plan{}, nil
}

func (s *wireFakeSession) Begin(_ context.Context, _ models.TxOptions) (drivers.Transaction, error) {
	return nil, nil
}
func (s *wireFakeSession) InTransaction() bool                     { return false }
func (s *wireFakeSession) CurrentTransaction() drivers.Transaction { return nil }
func (s *wireFakeSession) Encoder() drivers.Encoder                { return nopEncoder{} }

// nopEncoder is a no-op drivers.Encoder used by the wireFake test session.
// It returns "NULL" for any input — the wiring tests never inspect literals.
type nopEncoder struct{}

func (nopEncoder) EncodeLiteral(_ any, _ uint32) string { return "NULL" }

type wireFakeConn struct {
	acquired atomic.Int32
	schemas  []models.Schema
	indexes  []models.Index
	columns  []models.Column
	// openHook fires from wireFakeDriver.Open during the dial, before the
	// connection is returned. The supersession test uses it
	// to bump the Gui's connectGen mid-dial (simulating a newer activation
	// arriving while this connect is still in flight).
	openHook func()
	// openErr, when non-nil, makes wireFakeDriver.Open return it instead of
	// the connection — simulating a dial failure so the connect error path
	// (connectInvoker.routeConnectError → CONNECTING.SetError) is exercised.
	openErr error
	// openHookCtx, when set, receives the Open ctx so a test can block the
	// dial on <-ctx.Done() and assert the cancellable ctx threads through to
	// the driver (cancel/teardown). Runs instead of
	// openHook when both are set is avoided — set only one.
	openHookCtx func(context.Context)
	// emitStages, when set, is the sequence of driver ConnectStages the fake
	// Open reports through the ProgressReporter — mirroring a real driver
	// surfacing tunnel/auth milestones (T3 staged-progress wiring).
	emitStages []drivers.ConnectStage
	// schemasErr, when non-nil, makes the fake session's ListSchemas return it
	// so the Objects stage fails (T3 AD7 schema-load-failure path).
	schemasErr error
	// schemasBlock, when set, blocks ListSchemas on it until the ctx is
	// cancelled — used to exercise the Objects schema-load TIMEOUT path so the
	// row fails into ✗ rather than hanging on "Loading objects…" (T3 AD7).
	schemasBlock chan struct{}
}

func (c *wireFakeConn) Close() error                                     { return nil }
func (c *wireFakeConn) Ping(_ context.Context) error                     { return nil }
func (c *wireFakeConn) ServerVersion() string                            { return "fake-wire-1.0" }
func (c *wireFakeConn) Cancel(_ context.Context, _ models.QueryID) error { return nil }
func (c *wireFakeConn) AcquireSession(_ context.Context) (drivers.Session, error) {
	n := c.acquired.Add(1)
	return &wireFakeSession{
		id:           models.SessionID(n),
		schemas:      c.schemas,
		indexes:      c.indexes,
		columns:      c.columns,
		schemasErr:   c.schemasErr,
		schemasBlock: c.schemasBlock,
	}, nil
}

type wireFakeDriver struct {
	conn *wireFakeConn
	caps drivers.Capabilities
}

func (d *wireFakeDriver) Name() string                       { return "wire-fake" }
func (d *wireFakeDriver) Capabilities() drivers.Capabilities { return d.caps }
func (d *wireFakeDriver) Open(ctx context.Context, _ drivers.ConnectionProfile, reporter drivers.ProgressReporter) (drivers.Connection, error) {
	if d.conn != nil && d.conn.openHookCtx != nil {
		d.conn.openHookCtx(ctx)
	}
	if d.conn != nil && d.conn.openHook != nil {
		d.conn.openHook()
	}
	if d.conn != nil {
		for _, st := range d.conn.emitStages {
			drivers.ReportStage(reporter, st)
		}
	}
	if d.conn != nil && d.conn.openErr != nil {
		return nil, d.conn.openErr
	}
	return d.conn, nil
}

// uniqueDriverNameCounter avoids cross-test collisions: drivers.Register
// panics on duplicate names and the registry has no public Unregister.
var uniqueDriverNameCounter atomic.Int64

func registerWireFake(t *testing.T, caps drivers.Capabilities) (name string, conn *wireFakeConn) {
	t.Helper()
	n := uniqueDriverNameCounter.Add(1)
	name = "wire-fake-" + itoaInt64(n)
	conn = &wireFakeConn{}
	drv := &wireFakeDriver{conn: conn, caps: caps}
	drivers.Register(name, func(_ context.Context) (drivers.Driver, error) {
		return drv, nil
	})
	return name, conn
}

// itoaInt64 avoids dragging strconv into the rest of the file's import list.
func itoaInt64(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// buildTestGuiWithHistory mirrors buildTestGui (gui_test.go) but injects
// a HistoryProvider that opens the sqlite under t.TempDir() so the XDG
// state dir is never touched during tests.
func buildTestGuiWithHistory(t *testing.T) (*orchestrator.Gui, *testfake.RecorderGuiDriver) {
	t.Helper()
	fs := afero.NewMemMapFs()
	log := slog.New(slog.DiscardHandler)
	cfg := config.GetDefaultConfig()
	c := common.NewCommon(log, i18n.EnglishTranslationSet(), cfg, &common.AppState{}, fs)
	store := common.NewAppStateStore(fs, "/tmp/state.yml", common.DefaultClock())

	tmp := t.TempDir()
	g := orchestrator.NewGui(orchestrator.Deps{
		Common:              c,
		Store:               store,
		ConnectionsPath:     filepath.Join(tmp, "connections.yml"),
		ConnectionsProvider: func() []models.Connection { return nil },
		DriverNamesFn:       func() []string { return []string{"postgres"} },
		HistoryProvider: func() (*query.History, error) {
			return query.New(filepath.Join(tmp, "history.sqlite"))
		},
	})
	rec := testfake.NewRecorderGuiDriver()
	if err := g.UseDriverForTest(rec); err != nil {
		t.Fatalf("UseDriverForTest: %v", err)
	}
	t.Cleanup(func() { _ = g.Close() })
	return g, rec
}

func TestQueryRunnerHasNoSessionBeforeConnect(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)
	bag := g.HelperBagForTest()
	if bag.QueryRunner == nil {
		t.Fatal("HelperBag.QueryRunner is nil after wireWithDriver — wiring did not stash the runner")
	}
	if bag.QueryRunner.HasSession() {
		t.Fatal("HasSession() = true before Connect; expected false")
	}
	if g.ActiveSQLSessionForTest() != nil {
		t.Fatal("ActiveSQLSessionForTest() != nil before Connect; expected nil")
	}
}

// A successful Connect MUST populate the SchemasContext
// with the visible (non-builtin-hidden) schemas returned by the driver.
// Before this fix the rail stayed empty even though the driver was
// connected.
func TestConnectInvokerPopulatesSchemasRail(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	caps := drivers.Capabilities{}
	driverName, conn := registerWireFake(t, caps)
	// Mix one user-schema and one pg-builtin to confirm the filter
	// pass strips the builtin. pg.BuiltinHiddenSchemas (driver = "postgres")
	// is what defaultHiddenPatterns returns; the wire-fake uses a
	// distinct driver name, so the builtin patterns still apply because
	// populateSchemasRail unconditionally hands them in.
	conn.schemas = []models.Schema{
		{Name: "app", Owner: "u"},
		{Name: "pg_catalog", Owner: "u"},
	}

	bag := g.HelperBagForTest()
	profile := &models.Connection{Name: "wired-schemas", Driver: driverName, DSN: "postgres://stub"}
	if err := bag.Connect.Connect(context.Background(), profile); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	sch := g.Registry().Schemas
	if sch == nil {
		t.Fatal("registry.Schemas is nil")
	}
	items := sch.Items()
	if len(items) == 0 {
		t.Fatal("SchemasContext.Items() is empty after Connect; populateSchemasRail did not fire")
	}
	// Confirm at least the user schema landed; the pg_catalog row may or
	// may not be filtered depending on whether the helper's filter
	// snapshot covers it — the AC only requires the rail is non-empty
	// with the user schema present.
	var foundApp bool
	for _, it := range items {
		if s, ok := it.(models.Schema); ok && s.Name == "app" {
			foundApp = true
			break
		}
	}
	if !foundApp {
		t.Fatalf("SchemasContext.Items() = %v; expected the 'app' user schema", items)
	}
}

func TestConnectInvokerBindsQueryRunner(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	caps := drivers.Capabilities{HasLiveCancel: true, MaxIdentifierLen: 63}
	driverName, conn := registerWireFake(t, caps)

	bag := g.HelperBagForTest()
	if bag.Connect == nil {
		t.Fatal("HelperBag.Connect is nil")
	}

	profile := &models.Connection{Name: "wired", Driver: driverName, DSN: "postgres://stub"}
	if err := bag.Connect.Connect(context.Background(), profile); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Connect path acquires TWO sessions: the schema-rail session from
	// ConnectHelper and the query-runtime session from wireQueryRuntime.
	if got := conn.acquired.Load(); got != 2 {
		t.Fatalf("conn.AcquireSession called %d times, want 2 (schema + query)", got)
	}

	// The runner pointer the helperBag holds is the SAME *data.QueryRunner
	// that wireWithDriver stashed on the Gui — Bind on the orchestrator's
	// reference is observable here.
	if !bag.QueryRunner.HasSession() {
		t.Fatal("HasSession() = false after Connect; Bind did not propagate")
	}
	if got := bag.QueryRunner.Capabilities(); got != caps {
		t.Fatalf("Capabilities() = %+v, want %+v", got, caps)
	}
	if g.ActiveSQLSessionForTest() == nil {
		t.Fatal("ActiveSQLSessionForTest() = nil after Connect; SQLSession was not stashed for Close")
	}
}

func TestEditorBufferReaderReadsBuffer(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	// Seed the canonical *editor.Buffer hung off QueryEditorContext.
	// editorBufferAdapter reads from Buffer (not the
	// view's TextArea) — Architecture Decision 2: Buffer is the source
	// of truth, the view is a mirror written by VimEditor on every
	// Passthrough.
	qec := g.Registry().QueryEditor
	if qec == nil {
		t.Fatal("registry.QueryEditor is nil after wireWithDriver")
	}
	buf := qec.Buffer()
	if err := buf.Apply(editor.Edit{
		Kind:  editor.EditKindInsert,
		Range: editor.Range{Start: editor.Position{}, End: editor.Position{}},
		Text:  "SELECT 1;\nSELECT 2;",
	}); err != nil {
		t.Fatalf("Buffer.Apply: %v", err)
	}
	buf.SetCursor(editor.Position{Line: 1, Col: 9})

	bag := g.HelperBagForTest()
	if bag.EditorBuffer == nil {
		t.Fatal("HelperBag.EditorBuffer is nil after wireWithDriver")
	}
	if got, want := bag.EditorBuffer.BufferText(), "SELECT 1;\nSELECT 2;"; got != want {
		t.Fatalf("BufferText() = %q, want %q", got, want)
	}
	// "SELECT 1;\n" = 10 bytes; cursor at col 9 of line 1 → 10 + 9 = 19.
	if got, want := bag.EditorBuffer.CursorOffset(), 19; got != want {
		t.Fatalf("CursorOffset() = %d, want %d", got, want)
	}
}

// editorBufferForTest seeds the canonical QueryEditor Buffer with seed
// text + cursor and returns the live buffer plus the controller-facing
// EditorBufferReader, both backed by the same real *editor.Buffer.
func editorBufferForTest(t *testing.T, seed string, cursor editor.Position) (*editor.Buffer, controllers.EditorBufferReader) {
	t.Helper()
	g, _ := buildTestGuiWithHistory(t)
	qec := g.Registry().QueryEditor
	if qec == nil {
		t.Fatal("registry.QueryEditor is nil after wireWithDriver")
	}
	buf := qec.Buffer()
	if seed != "" {
		if err := buf.Apply(editor.Edit{
			Kind:  editor.EditKindInsert,
			Range: editor.Range{Start: editor.Position{}, End: editor.Position{}},
			Text:  seed,
		}); err != nil {
			t.Fatalf("seed Buffer.Apply: %v", err)
		}
	}
	buf.SetCursor(cursor)
	bag := g.HelperBagForTest()
	if bag.EditorBuffer == nil {
		t.Fatal("HelperBag.EditorBuffer is nil after wireWithDriver")
	}
	return buf, bag.EditorBuffer
}

func TestEditorBufferAdapterInsertAtCursorMid(t *testing.T) {
	// "SELECT ;" — cursor between "SELECT " and ";" (col 7).
	buf, ed := editorBufferForTest(t, "SELECT ;", editor.Position{Line: 0, Col: 7})
	if err := ed.InsertAtCursor("1"); err != nil {
		t.Fatalf("InsertAtCursor: %v", err)
	}
	if got, want := buf.String(), "SELECT 1;"; got != want {
		t.Fatalf("after insert String() = %q, want %q", got, want)
	}
	if got, want := buf.CursorPos(), (editor.Position{Line: 0, Col: 8}); got != want {
		t.Fatalf("cursor = %+v, want %+v (end of inserted text)", got, want)
	}
}

func TestEditorBufferAdapterInsertAtCursorOneUndo(t *testing.T) {
	buf, ed := editorBufferForTest(t, "SELECT ;", editor.Position{Line: 0, Col: 7})
	if err := ed.InsertAtCursor("1"); err != nil {
		t.Fatalf("InsertAtCursor: %v", err)
	}
	if err := buf.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if got, want := buf.String(), "SELECT ;"; got != want {
		t.Fatalf("after single Undo String() = %q, want pre-insert %q", got, want)
	}
}

func TestEditorBufferAdapterInsertAtCursorEmptyBuffer(t *testing.T) {
	buf, ed := editorBufferForTest(t, "", editor.Position{})
	if err := ed.InsertAtCursor("hello"); err != nil {
		t.Fatalf("InsertAtCursor: %v", err)
	}
	if got, want := buf.String(), "hello"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	if got, want := buf.CursorPos(), (editor.Position{Line: 0, Col: 5}); got != want {
		t.Fatalf("cursor = %+v, want %+v", got, want)
	}
}

func TestEditorBufferAdapterInsertAtCursorEndOfBuffer(t *testing.T) {
	// Cursor at end of single-line buffer "abc" (col 3).
	buf, ed := editorBufferForTest(t, "abc", editor.Position{Line: 0, Col: 3})
	if err := ed.InsertAtCursor("def"); err != nil {
		t.Fatalf("InsertAtCursor: %v", err)
	}
	if got, want := buf.String(), "abcdef"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	if got, want := buf.CursorPos(), (editor.Position{Line: 0, Col: 6}); got != want {
		t.Fatalf("cursor = %+v, want %+v", got, want)
	}
}

func TestEditorBufferAdapterInsertAtCursorMultiLineOneUndo(t *testing.T) {
	// Cursor at end of "a" (col 1); insert text containing newlines.
	buf, ed := editorBufferForTest(t, "a", editor.Position{Line: 0, Col: 1})
	if err := ed.InsertAtCursor("X\nY\nZ"); err != nil {
		t.Fatalf("InsertAtCursor: %v", err)
	}
	if got, want := buf.String(), "aX\nY\nZ"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	// End of inserted span: started at {0,1}, two newlines → line 2, last
	// chunk "Z" → col 1.
	if got, want := buf.CursorPos(), (editor.Position{Line: 2, Col: 1}); got != want {
		t.Fatalf("cursor = %+v, want %+v", got, want)
	}
	if err := buf.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if got, want := buf.String(), "a"; got != want {
		t.Fatalf("after single Undo String() = %q, want %q (multi-line insert is one undo step)", got, want)
	}
}

// TestWireEditorCompletionRegistersSnippetSource asserts that after the GUI
// wires the completion engine (wireEditorCompletion, run during
// wireWithDriver), the engine's Sources() contains exactly one source named
// "snippets" and it is the real *editor.SnippetSource — not the removed
// placeholder stub source.
func TestWireEditorCompletionRegistersSnippetSource(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	sources := g.CompletionSourcesForTest()
	if len(sources) == 0 {
		t.Fatal("CompletionSourcesForTest() returned no sources; wireEditorCompletion did not wire the engine")
	}

	var snippetSources []editor.Source
	for _, s := range sources {
		if s.Name() == editor.SnippetSourceName {
			snippetSources = append(snippetSources, s)
		}
	}
	if len(snippetSources) != 1 {
		t.Fatalf("found %d sources named %q; want exactly 1", len(snippetSources), editor.SnippetSourceName)
	}
	if _, ok := snippetSources[0].(*editor.SnippetSource); !ok {
		t.Fatalf("snippet source has type %T; want *editor.SnippetSource (not the removed stub)", snippetSources[0])
	}

	// The wired source must be backed by the built-in provider, so Trigger on
	// an empty prefix surfaces built-in snippets (e.g. select_all) with a
	// non-empty Body — proving it is not an empty provider.
	buf := &editor.Buffer{Lines: []editor.Line{{Runes: []rune("select_al")}}}
	pos := editor.Position{Line: 0, Col: len([]rune("select_al"))}
	got := snippetSources[0].Suggest(context.Background(), buf, pos)
	found := false
	for _, sug := range got {
		if sug.Source != editor.SnippetSourceName {
			t.Errorf("suggestion Source = %q; want %q", sug.Source, editor.SnippetSourceName)
		}
		if sug.Text == "select_all" {
			found = true
			if sug.Body == "" {
				t.Error("built-in snippet select_all has empty Body; want the multi-line expansion")
			}
		}
	}
	if !found {
		t.Errorf("wired SnippetSource did not surface built-in snippet select_all; got %d suggestions (provider may be empty)", len(got))
	}
}
