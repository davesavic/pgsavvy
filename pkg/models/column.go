package models

// Column describes a single column of a Table.
type Column struct {
	Name         string
	DataType     string
	Default      string
	Nullable     bool
	IsPrimaryKey bool
	Position     int
	Description  string
}
