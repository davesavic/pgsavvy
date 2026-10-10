package models

// ColumnMeta describes a result-set column returned by a RowStream. See
// DESIGN.md §11.1 (RowStream.Columns()).
type ColumnMeta struct {
	Name string
	// PhysicalName is catalog-resolved by relation OID and attribute number.
	// Name remains the display label; an empty PhysicalName is unresolved.
	PhysicalName         string
	TypeOID              uint32
	TypeName             string
	Nullable             bool
	TableOID             uint32
	TableAttributeNumber uint16
	IsForeignKey         bool
}
