package session

import (
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// uuidCodec wraps pgx's built-in UUIDCodec, reusing it for all wire
// encode/decode work and overriding only DecodeValue's return type. The
// default codec decodes a uuid column into a bare [16]byte, which the grid
// renders as Go's "[85 14 132 ...]" byte-array notation; returning a
// google/uuid.UUID (a fmt.Stringer) instead makes rows.Values() yield a value
// whose canonical "550e8400-..." string is what every consumer sees. See the
// uuid regression test in pkg/drivers/pg.
type uuidCodec struct {
	pgtype.UUIDCodec
}

func (c uuidCodec) DecodeValue(m *pgtype.Map, oid uint32, format int16, src []byte) (any, error) {
	v, err := c.UUIDCodec.DecodeValue(m, oid, format, src)
	if err != nil || v == nil {
		return v, err
	}
	b, ok := v.([16]byte)
	if !ok {
		return v, nil
	}
	return uuid.UUID(b), nil
}

// registerUUIDType installs uuidCodec for the uuid scalar and re-points the
// uuid[] array codec at it so array elements decode canonically too. Called
// from BuildPgxConfig's AfterConnect hook on every pooled connection.
func registerUUIDType(m *pgtype.Map) {
	m.RegisterType(&pgtype.Type{Name: "uuid", OID: pgtype.UUIDOID, Codec: uuidCodec{}})
	if elem, ok := m.TypeForOID(pgtype.UUIDOID); ok {
		m.RegisterType(&pgtype.Type{
			Name:  "_uuid",
			OID:   pgtype.UUIDArrayOID,
			Codec: &pgtype.ArrayCodec{ElementType: elem},
		})
	}
}
