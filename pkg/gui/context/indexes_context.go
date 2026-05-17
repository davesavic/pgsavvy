package context

// IndexesContext renders the index list in the left-rail INDEXES slot.
type IndexesContext struct {
	SideListContext
}

// NewIndexesContext builds an IndexesContext bound to the INDEXES key and view.
func NewIndexesContext(base BaseContext, deps Deps) *IndexesContext {
	return &IndexesContext{
		SideListContext: NewSideListContext(base, deps),
	}
}
