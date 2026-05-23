package models

// ColumnMeta describes a result-set column returned by a RowStream. See
// DESIGN.md §11.1 (RowStream.Columns()).
type ColumnMeta struct {
	Name                 string
	TypeOID              uint32
	TypeName             string
	Nullable             bool
	TableOID             uint32
	TableAttributeNumber uint16
	IsForeignKey         bool
}
