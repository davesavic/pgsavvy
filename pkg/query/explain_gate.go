package query

import "regexp"

// dmlTokenRE matches a whole-word, case-insensitive DML keyword anywhere in a
// statement. It exists to close the writable-CTE hole: a statement like
//
//	WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d
//
// leads with WITH and therefore classifies as KindOther, yet it executes a
// write. Scanning the whole statement for an embedded INSERT/UPDATE/DELETE/MERGE
// token lets EffectiveAnalyze fail closed on these. Whole-word boundaries keep
// identifiers such as a column named "updated_at" from matching.
var dmlTokenRE = regexp.MustCompile(`(?i)\b(INSERT|UPDATE|DELETE|MERGE)\b`)

// EffectiveAnalyze decides whether an EXPLAIN ANALYZE (which executes the
// statement) is safe to run. It is a fail-closed gate, pure and reusable.
//
// ANALYZE is permitted only when it was requested AND either:
//   - the connection is read-only (the server itself rejects writes, so an
//     accidental write statement cannot mutate data), or
//   - the statement is not a DML/DDL statement (Classify == KindOther) and no
//     whole-word INSERT/UPDATE/DELETE/MERGE token appears anywhere in it
//     (closing the writable-CTE hole).
//
// readOnly is passed as a bool rather than a *models.Connection to keep this
// package free of a models dependency and to keep the helper trivially
// testable.
func EffectiveAnalyze(sql string, readOnly bool, requested bool) bool {
	if !requested {
		return false
	}
	if readOnly {
		return true
	}
	if Classify(sql) != KindOther {
		return false
	}
	return !dmlTokenRE.MatchString(sql)
}
