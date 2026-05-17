package context

// TablesContext renders the table list in the left-rail TABLES slot. It
// embeds SideListContext for cursor and row management; no extra fields
// are needed at this layer — table fetching and per-row rendering are
// supplied by helpers/controllers in later epics.
type TablesContext struct {
	SideListContext
}

// NewTablesContext builds a TablesContext bound to the TABLES key and view.
func NewTablesContext(base BaseContext, deps Deps) *TablesContext {
	return &TablesContext{
		SideListContext: NewSideListContext(base, deps),
	}
}
