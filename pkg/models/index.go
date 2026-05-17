package models

// Index describes an index defined on a Table.
type Index struct {
	Name      string
	Schema    string
	Table     string
	Columns   []string
	IsUnique  bool
	IsPrimary bool
	Method    string
}
