package context

// ColumnsContext renders the column list in the left-rail COLUMNS slot.
type ColumnsContext struct {
	SideListContext
}

// NewColumnsContext builds a ColumnsContext bound to the COLUMNS key and view.
func NewColumnsContext(base BaseContext, deps Deps) *ColumnsContext {
	return &ColumnsContext{
		SideListContext: NewSideListContext(base, deps),
	}
}
