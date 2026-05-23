package models

// ForeignKey describes a single foreign-key constraint on a table.
type ForeignKey struct {
	// Name is the constraint name as reported by the database.
	Name string
	// Schema is the schema of the owning (referencing) table.
	Schema string
	// Table is the owning (referencing) table.
	Table string
	// Columns are the referencing columns, in order.
	Columns []string
	// RefSchema is the schema of the referenced table.
	RefSchema string
	// RefTable is the referenced table.
	RefTable string
	// RefColumns are the referenced columns, in order, paired positionally
	// with Columns.
	RefColumns []string
	// OnDelete is the referential action on delete (e.g. "NO ACTION",
	// "CASCADE", "SET NULL").
	OnDelete string
	// OnUpdate is the referential action on update.
	OnUpdate string
}
