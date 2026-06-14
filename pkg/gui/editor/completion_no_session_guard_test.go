package editor

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// driverSessionMethods are the method names on drivers.Session that fetch
// schema/function metadata. The jxw data race was caused by completion sources
// calling these DIRECTLY on a live drivers.Session from the UI goroutine,
// bypassing the per-Session serialization contract. Every
// completion source was repointed at the synchronous SchemaMetadata snapshot interface, so NO
// completion_*.go path may name any of these (the warmer is the only caller, and
// it routes them through the serialized ConnectHelper wrappers).
var driverSessionMethods = []string{
	"ListDatabases",
	"ListSchemas",
	"ListTables",
	"ListColumns",
	"ListIndexes",
	"ListConstraints",
	"ListForeignKeys",
	"ListInboundForeignKeys",
	"ListFunctions",
	"DescribeFunction",
	"Execute",
	"Stream",
	"Explain",
}

// TestCompletion_NoDirectSession_GrepGuard formalizes jxw success criterion #1
// ("No completion code path calls drivers.Session methods directly") as a
// failing-on-regression source scan. It reads every non-test completion_*.go in
// this package and asserts that none of them:
//   - imports github.com/davesavic/dbsavvy/pkg/drivers, or
//   - calls any drivers.Session metadata method.
//
// A future edit that reintroduces a direct session call from a completion source
// (the exact jxw regression) fails this test before it can ship.
func TestCompletion_NoDirectSession_GrepGuard(t *testing.T) {
	files, err := filepath.Glob("completion_*.go")
	require.NoError(t, err)
	require.NotEmpty(t, files, "expected completion_*.go sources in package editor")

	const driversImport = "github.com/davesavic/dbsavvy/pkg/drivers"

	fset := token.NewFileSet()
	scanned := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		scanned++

		f, perr := parser.ParseFile(fset, file, nil, parser.ImportsOnly)
		require.NoError(t, perr, "parse imports %s", file)
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			require.NotEqual(t, driversImport, path,
				"%s imports pkg/drivers — completion sources must read the SchemaMetadata snapshot, not a live drivers.Session (jxw)", file)
		}

		// Full parse for an AST identifier sweep: catch any drivers.Session method
		// name appearing as a selector (e.g. sess.ListColumns(...)). Reading the
		// snapshot interface uses Columns/TableNames/FunctionNames/ForeignKeys —
		// distinct names — so a hit here is a genuine direct-session call.
		src, rerr := os.ReadFile(file)
		require.NoError(t, rerr)
		full, perr := parser.ParseFile(fset, file, src, 0)
		require.NoError(t, perr, "parse %s", file)

		ast.Inspect(full, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			for _, m := range driverSessionMethods {
				require.NotEqualf(t, m, sel.Sel.Name,
					"%s calls a drivers.Session method %q — completion must go through the snapshot, not a live session (jxw)", file, m)
			}
			return true
		})
	}
	require.Positive(t, scanned, "no non-test completion_*.go files were scanned")
}

// TestCompletion_SourcesConstructedWithSnapshot_NotSession is the construction
// half of the guard: it asserts the source types HOLD the SchemaMetadata snapshot
// interface (and the SchemaSource the TableWarmer), not a raw drivers.Session.
// Because NewSchemaSource / NewFunctionSource only accept the snapshot interfaces,
// a source physically cannot be wired with a live session — the type system
// enforces jxw criterion #1 at the construction site.
func TestCompletion_SourcesConstructedWithSnapshot_NotSession(t *testing.T) {
	meta := newFakeMeta()
	warmer := &fakeWarmer{}

	// These compile-and-run only because the constructors take the snapshot
	// interfaces. The assignments below pin the static types.
	var _ SchemaMetadata = meta
	var _ TableWarmer = warmer

	schemaSrc := NewSchemaSource(meta, warmer, schemaProv("public"))
	require.Same(t, meta, schemaSrc.meta, "SchemaSource must hold the injected SchemaMetadata snapshot")
	require.Same(t, warmer, schemaSrc.warmer, "SchemaSource must hold the injected TableWarmer, not a session")

	fnSrc := NewFunctionSource(meta)
	require.Same(t, meta, fnSrc.meta, "FunctionSource must hold the injected SchemaMetadata snapshot")
}
