// Editability introspection — F2.
//
// After a SELECT opens a row stream, the result tabs helper (Z1 scope) calls
// EditabilityIntrospect with the column metadata harvested from pgx
// FieldDescriptions. This package answers two questions: (a) does the result
// trace back to a single editable base relation, and (b) which SELECT-order
// indexes form the minimal row-identity set used by A5/B5/B6 to emit
// UPDATE / DELETE / INSERT statements.
//
// The DisabledReason strings are part of the cross-task contract —
// do not change them without coordinating the consumers.

package pg

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// Frozen DisabledReason strings. Consumed verbatim by A5/B5/B6/Z1.
const (
	disabledReasonMultiTable        = "result spans multiple tables"
	disabledReasonComputed          = "result contains computed columns"
	disabledReasonView              = "base relation is a view"
	disabledReasonMatView           = "materialized view"
	disabledReasonForeign           = "foreign table"
	disabledReasonPartitioned       = "partitioned table"
	disabledReasonTemp              = "temporary table"
	disabledReasonNoRowIdentity     = "no row identity"
	disabledReasonReadOnly          = "read-only connection"
	disabledReasonNoInlineEdit      = "driver does not support inline edit"
	disabledReasonIntrospectFailFmt = "introspection failed: %w"
)

// relRow is the per-OID payload returned by the introspection SQL. Exposed
// to the unit test through the unexported `decideEditability` entry point so
// the SQL roundtrip can be stubbed without bringing up pgx.
type relRow struct {
	OID           uint32
	RelKind       string // "r","v","m","f","p","t",...
	Schema        string
	Name          string
	IsTempSchema  bool
	PKAttnums     []int32   // primary-key attnums, nil if none
	UniqueAttnums [][]int32 // per-UNIQUE-index attnum sets, nil if none
}

// relIntrospector is the SQL-bound dependency factored out so unit tests can
// drive decideEditability without a live database.
type relIntrospector func(ctx context.Context, oids []uint32) ([]relRow, error)

// EditabilityIntrospect inspects the column metadata of a query result and
// decides whether it is editable. See package-level docstring for contract.
//
// The session inFlight guard is acquired for the duration of the call,
// matching ListConstraints / ListIndexes.
func EditabilityIntrospect(
	ctx context.Context,
	sess *Session,
	cols []models.ColumnMeta,
) (baseRelation models.Ref, rowIdentity []int, disabledReason string, err error) {
	defer sess.guard()()
	return decideEditability(ctx, cols, func(ctx context.Context, oids []uint32) ([]relRow, error) {
		return runEditabilityIntrospect(ctx, sess, oids)
	})
}

// decideEditability is the pure-logic core. It assumes the inFlight guard is
// already held (production: by EditabilityIntrospect; tests: irrelevant).
func decideEditability(
	ctx context.Context,
	cols []models.ColumnMeta,
	introspect relIntrospector,
) (baseRelation models.Ref, rowIdentity []int, disabledReason string, err error) {
	if len(cols) == 0 {
		return models.Ref{}, nil, disabledReasonComputed, nil
	}

	// Any column missing a TableOID is a computed expression (literal,
	// function call, arithmetic, …) which by itself disqualifies the
	// result from inline edit.
	for _, c := range cols {
		if c.TableOID == 0 {
			return models.Ref{}, nil, disabledReasonComputed, nil
		}
	}

	// Collect distinct TableOIDs in first-seen order.
	seen := make(map[uint32]struct{}, 4)
	var oids []uint32
	for _, c := range cols {
		if _, ok := seen[c.TableOID]; ok {
			continue
		}
		seen[c.TableOID] = struct{}{}
		oids = append(oids, c.TableOID)
	}
	if len(oids) > 1 {
		return models.Ref{}, nil, disabledReasonMultiTable, nil
	}

	rows, ierr := introspect(ctx, oids)
	if ierr != nil {
		wrapped := fmt.Errorf(disabledReasonIntrospectFailFmt, ierr)
		return models.Ref{}, nil, wrapped.Error(), wrapped
	}
	if len(rows) == 0 {
		// Nothing came back from pg_class for our OID — treat as an
		// introspection failure so the caller surfaces a clear reason.
		wrapped := fmt.Errorf(disabledReasonIntrospectFailFmt, errors.New("relation not found"))
		return models.Ref{}, nil, wrapped.Error(), wrapped
	}
	r := rows[0]
	ref := models.Ref{Schema: r.Schema, Table: r.Name}

	// Map relkind / temp schema to a frozen reason. Order: temp wins over
	// relkind because pg_temp schemas can host base tables (relkind='r')
	// that are still off-limits for inline edit.
	if r.IsTempSchema {
		return ref, nil, disabledReasonTemp, nil
	}
	switch r.RelKind {
	case "v":
		return ref, nil, disabledReasonView, nil
	case "m":
		return ref, nil, disabledReasonMatView, nil
	case "f":
		return ref, nil, disabledReasonForeign, nil
	case "p":
		return ref, nil, disabledReasonPartitioned, nil
	case "r":
		// editable candidate — fall through to row-identity resolution
	default:
		// Any other relkind (composite type, sequence, toast, index, …)
		// is not editable. Surface it as "no row identity" — the most
		// specific frozen reason that fits.
		return ref, nil, disabledReasonNoRowIdentity, nil
	}

	rowID, ok := computeRowIdentity(cols, r.PKAttnums, r.UniqueAttnums)
	if !ok {
		return ref, nil, disabledReasonNoRowIdentity, nil
	}
	return ref, rowID, "", nil
}

// computeRowIdentity picks the smallest PK/UNIQUE index whose attnums are
// ALL present in the SELECT list, and returns the SELECT-order indexes of
// those columns. PK is preferred over UNIQUE when both are equally small.
// When a column appears more than once in the SELECT list, the lowest index
// occurrence is used.
func computeRowIdentity(
	cols []models.ColumnMeta,
	pkAttnums []int32,
	uniqueAttnums [][]int32,
) ([]int, bool) {
	// attnum → lowest SELECT-order index.
	first := make(map[uint16]int, len(cols))
	for i, c := range cols {
		if c.TableAttributeNumber == 0 {
			continue
		}
		if _, seen := first[c.TableAttributeNumber]; !seen {
			first[c.TableAttributeNumber] = i
		}
	}

	candidates := make([][]int32, 0, 1+len(uniqueAttnums))
	if len(pkAttnums) > 0 {
		candidates = append(candidates, pkAttnums)
	}
	for _, u := range uniqueAttnums {
		if len(u) > 0 {
			candidates = append(candidates, u)
		}
	}
	if len(candidates) == 0 {
		return nil, false
	}

	// Sort candidates by size — smallest indexes win; PK already sits at
	// the front when present.
	sort.SliceStable(candidates, func(i, j int) bool {
		return len(candidates[i]) < len(candidates[j])
	})

	for _, attnums := range candidates {
		idxs := make([]int, 0, len(attnums))
		ok := true
		for _, a := range attnums {
			if a <= 0 {
				ok = false
				break
			}
			pos, found := first[uint16(a)]
			if !found {
				ok = false
				break
			}
			idxs = append(idxs, pos)
		}
		if !ok {
			continue
		}
		sort.Ints(idxs)
		return idxs, true
	}
	return nil, false
}

// runEditabilityIntrospect dispatches the embedded SQL and scans rows into
// relRow values. Lives next to the pure decideEditability so the
// SQL-dependent surface stays small.
func runEditabilityIntrospect(ctx context.Context, sess *Session, oids []uint32) ([]relRow, error) {
	// $2 is an empty smallint[] placeholder — the column-attnum array is
	// currently consumed Go-side, but the parameter slot stays in the
	// wire shape for forward compatibility (see editability_introspect.sql).
	rows, err := sess.conn.Query(ctx, sqlEditabilityIntrospect, oids, []int16{})
	if err != nil {
		return nil, wrapPgError(err)
	}
	defer rows.Close()

	var out []relRow
	for rows.Next() {
		var (
			r          relRow
			pkAttnums  []int32
			uniqueSets [][]int32
		)
		if scanErr := rows.Scan(
			&r.OID,
			&r.RelKind,
			&r.Schema,
			&r.Name,
			&r.IsTempSchema,
			&pkAttnums,
			&uniqueSets,
		); scanErr != nil {
			return nil, wrapPgError(scanErr)
		}
		r.PKAttnums = pkAttnums
		r.UniqueAttnums = uniqueSets
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapPgError(err)
	}
	return out, nil
}

// ApplyConnectionGate folds connection-level overrides on top of a
// relation-level editability decision. read_only beats every other reason
// (the user has explicitly opted out of writes); driver-level
// SupportsInlineEdit=false is reported when the relation would otherwise be
// editable. The original `currentReason` is preserved when neither override
// applies.
//
// Precedence: read-only > !SupportsInlineEdit > currentReason.
func ApplyConnectionGate(
	editable bool,
	currentReason string,
	connReadOnly bool,
	supportsInlineEdit bool,
) (bool, string) {
	if connReadOnly {
		return false, disabledReasonReadOnly
	}
	if !supportsInlineEdit {
		return false, disabledReasonNoInlineEdit
	}
	return editable, currentReason
}
