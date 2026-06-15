package orchestrator_test

import (
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
)

// TestSetRunnerWiredToSetHandler guards the setExHandler triple-use:
// after wireWithDriver, the :set ex-command,
// SearchPath.SetRunner, and StatementTimeout.SetRunner must all be wired to
// g.handleSetEx. Func values are not comparable, so we assert both runners are
// non-nil and route through handleSetEx's empty-args guard path (which returns
// nil before touching the SQL session). A future edit that drops either
// SetRunner assignment compiles fine but fails this test.
func TestSetRunnerWiredToSetHandler(t *testing.T) {
	g, _ := buildTestGui(t)

	sp := g.SearchPathSetRunnerForTest()
	if sp == nil {
		t.Fatal("SearchPath.SetRunner is nil; :set wiring was dropped")
	}
	st := g.StatementTimeoutSetRunnerForTest()
	if st == nil {
		t.Fatal("StatementTimeout.SetRunner is nil; :set wiring was dropped")
	}

	// handleSetEx with empty args returns nil after a guard toast, without
	// needing an active session — proves both runners route to the SET handler.
	if err := sp(nil, commands.ExecCtx{}); err != nil {
		t.Fatalf("SearchPath.SetRunner(empty args): want nil, got %v", err)
	}
	if err := st(nil, commands.ExecCtx{}); err != nil {
		t.Fatalf("StatementTimeout.SetRunner(empty args): want nil, got %v", err)
	}
}
