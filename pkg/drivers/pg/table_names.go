package pg

import "context"

// TableNamesFromOIDs resolves a set of relation OIDs to their unqualified
// relation names via pg_class. OIDs with no matching relation are omitted
// from the returned map (callers degrade to the bare column name). An
// empty input returns a nil map without touching the connection.
//
// The session inFlight guard is held for the call, matching the other
// introspection helpers (EditabilityIntrospect / ListIndexes / ...).
func TableNamesFromOIDs(ctx context.Context, sess *Session, oids []uint32) (map[uint32]string, error) {
	if len(oids) == 0 {
		return nil, nil
	}
	defer sess.guard()()

	rows, err := sess.conn.Query(ctx, sqlTableNamesByOID, oids)
	if err != nil {
		return nil, wrapPgError(err)
	}
	defer rows.Close()

	out := make(map[uint32]string, len(oids))
	for rows.Next() {
		var (
			oid  uint32
			name string
		)
		if scanErr := rows.Scan(&oid, &name); scanErr != nil {
			return nil, wrapPgError(scanErr)
		}
		out[oid] = name
	}
	if err := rows.Err(); err != nil {
		return nil, wrapPgError(err)
	}
	return out, nil
}
