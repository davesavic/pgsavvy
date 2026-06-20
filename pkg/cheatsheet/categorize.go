package cheatsheet

// CategoryView is the cheatsheet output for a single functional Category. It
// reuses the ModeView/Section/Row types verbatim — CurrentScope and Global
// carry exactly the rows from the corresponding Output partition whose
// section Tag maps to this Category (per CategoryFor). The per-Mode partition
// and the per-Tag Sections within each Mode are preserved unchanged.
type CategoryView struct {
	Category     Category
	CurrentScope []ModeView
	Global       []ModeView
}

// Categorize buckets an Output's Sections into per-Category views. It is
// additive over the already-scope-filtered Output: every Row lands in exactly
// one CategoryView (CategoryFor is a function of the tag-homogeneous
// section Tag), with no loss, duplication, or field mutation.
//
// Non-General categories appear only when they carry ≥1 row. CategoryGeneral
// is ALWAYS present as the always-on catch-all (it hosts the empty-state body
// downstream) and sorts last per Categories order. Result order follows
// Categories.
func Categorize(out Output) []CategoryView {
	views := make([]CategoryView, 0, len(Categories))
	for _, c := range Categories {
		view := CategoryView{
			Category:     c,
			CurrentScope: filterByCategory(out.CurrentScope, c),
			Global:       filterByCategory(out.Global, c),
		}
		if c == CategoryGeneral {
			views = append(views, view)
			continue
		}
		if len(view.CurrentScope) == 0 && len(view.Global) == 0 {
			continue
		}
		views = append(views, view)
	}
	return views
}

// filterByCategory keeps, for each ModeView, only the Sections whose Tag maps
// to c (via CategoryFor), copying Rows verbatim. ModeViews left with zero
// sections are dropped.
func filterByCategory(views []ModeView, c Category) []ModeView {
	out := make([]ModeView, 0, len(views))
	for _, mv := range views {
		sections := make([]Section, 0, len(mv.Sections))
		for _, sect := range mv.Sections {
			if CategoryFor(sect.Tag) != c {
				continue
			}
			sections = append(sections, sect)
		}
		if len(sections) == 0 {
			continue
		}
		out = append(out, ModeView{Mode: mv.Mode, Sections: sections})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
