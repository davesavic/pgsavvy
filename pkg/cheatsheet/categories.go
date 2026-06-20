package cheatsheet

import "github.com/davesavic/pgsavvy/pkg/i18n"

// Category is a coarse functional bucket for cheatsheet command tags. The
// taxonomy is total and ordered: every Command.Tag (including the empty
// string and any future un-categorized tag) maps to exactly one Category
// via CategoryFor, and Categories lists them in a stable display order.
type Category string

const (
	CategoryEditing Category = "Editing"
	CategoryQuery   Category = "Query"
	CategoryResults Category = "Results"
	CategoryCells   Category = "Cells"
	CategorySession Category = "Session"
	CategoryGeneral Category = "General"
)

// Categories is the stable display order of all Categories. General sorts
// last as the catch-all bucket.
var Categories = []Category{
	CategoryEditing,
	CategoryQuery,
	CategoryResults,
	CategoryCells,
	CategorySession,
	CategoryGeneral,
}

// categoryByTag is the explicit Tag→Category mapping. It must contain an
// entry for every production tag in commands.KnownTags. Tags absent from
// this map (the empty string, or any un-categorized tag) fall back to
// CategoryGeneral via CategoryFor. General is therefore NOT a key here.
var categoryByTag = map[string]Category{
	"Edit":         CategoryEditing,
	"Insert":       CategoryEditing,
	"Visual":       CategoryEditing,
	"Operator":     CategoryEditing,
	"Motion":       CategoryEditing,
	"Text object":  CategoryEditing,
	"Edit history": CategoryEditing,

	"Query": CategoryQuery,
	"Plan":  CategoryQuery,

	"Result": CategoryResults,
	"Row":    CategoryResults,

	"Cell Edit": CategoryCells,
	"Commit":    CategoryCells,
	"Conflict":  CategoryCells,

	"Transaction": CategorySession,
	"Session":     CategorySession,
	"Connection":  CategorySession,
	"Help":        CategorySession,
}

// CategoryFor returns the Category for a command tag. The mapping is total
// and never panics: explicit entries win, everything else (including the
// empty string and unknown tags) falls back to CategoryGeneral.
func CategoryFor(tag string) Category {
	if c, ok := categoryByTag[tag]; ok {
		return c
	}
	return CategoryGeneral
}

// LabelFor resolves the localized display label for a Category from the
// given TranslationSet. CategoryGeneral is the catch-all label.
func LabelFor(c Category, tr *i18n.TranslationSet) string {
	switch c {
	case CategoryEditing:
		return tr.CheatsheetCategoryEditing
	case CategoryQuery:
		return tr.CheatsheetCategoryQuery
	case CategoryResults:
		return tr.CheatsheetCategoryResults
	case CategoryCells:
		return tr.CheatsheetCategoryCells
	case CategorySession:
		return tr.CheatsheetCategorySession
	default:
		return tr.CheatsheetCategoryGeneral
	}
}
