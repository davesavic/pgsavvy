package grid

import "fmt"

// emptyResultText is the placeholder shown when the result has no columns
// to render. A SELECT that returns nothing keeps its column headers, so it
// never reaches here — only DML without RETURNING produces a column-less
// result. When such a statement changed rows, the command tag carries the
// count and we surface "(N row(s) affected)" rather than the misleading
// "(0 rows)", which reads as "nothing happened". dbsavvy-outq.
func emptyResultText(snap viewSnapshot) string {
	if snap.rowsAffected <= 0 {
		return EmptyResultIndicator
	}
	if snap.rowsAffected == 1 {
		return "(1 row affected)"
	}
	return fmt.Sprintf("(%d rows affected)", snap.rowsAffected)
}
