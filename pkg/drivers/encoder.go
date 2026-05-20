package drivers

// Encoder turns a Go value into a SQL literal suitable for inlining into a
// statement targeted at a specific server. Implementations are stateless and
// safe for concurrent use; a Session holds a singleton instance.
//
// Encoder lives on drivers.Session (NOT drivers.Driver) because the correct
// encoding can depend on session-scoped state — notably PostgreSQL's
// standard_conforming_strings and server_encoding GUCs. Even when an
// implementation chooses an encoding form that happens to be invariant of
// those settings (as the pg implementation does, by always emitting the
// E'…' string form), the contract still belongs to the session.
//
// typeOID is the source-of-truth type identifier from the originating column
// when one is available (e.g. when re-encoding a result row for INSERT
// generation). Implementations MAY use it to disambiguate values whose Go
// type alone is insufficient (e.g. distinguishing json/jsonb from text). A
// zero value means "infer from val's Go type".
type Encoder interface {
	EncodeLiteral(val any, typeOID uint32) string
}
