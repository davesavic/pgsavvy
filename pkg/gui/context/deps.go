package context

import (
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// depsAlias is the package-local alias for types.ContextTreeDeps. Aliased
// (not redeclared) so the field bag is identical across the boundary;
// downstream tasks add fields to types.ContextTreeDeps without touching
// this file.
type depsAlias = types.ContextTreeDeps

// writeView runs fn on the driver MainLoop iff deps.GuiDriver is non-nil.
// All concrete contexts that perform view writes go through this helper
// so the nil-driver case (unit tests, partial wiring) is a silent no-op
// rather than a panic.
func writeView(deps depsAlias, fn func() error) {
	if deps.GuiDriver == nil {
		return
	}
	deps.GuiDriver.Update(fn)
}
