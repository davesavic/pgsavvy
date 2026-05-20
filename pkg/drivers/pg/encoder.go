package pg

import (
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/davesavic/dbsavvy/pkg/drivers"
)

// PostgreSQL system OIDs used for type-aware encoding. Keeping a tiny local
// list rather than importing pgtype's symbol table — these values are fixed
// in pg_type.h and have not changed across major versions.
const (
	jsonOID  uint32 = 114
	jsonbOID uint32 = 3802
)

// encoder is the stateless drivers.Encoder implementation for Postgres. A
// singleton is held on *Session and returned by Session.Encoder().
//
// All string and bytea output uses the E'…' string form. This is invariant
// of the server's standard_conforming_strings GUC: in both settings the
// E-form interprets backslash escapes the same way. The encoder therefore
// produces stable output across sessions regardless of GUC value.
//
// The implementation is hand-rolled rather than delegating to pgx's
// internal pgtype API because that API is unstable across pgx minor
// versions (e.g. pgtype.Map.Encode does not exist in v5 — see epic
// dbsavvy-uv0 AMENDMENTS).
type encoder struct{}

// pgEncoder is the package singleton — encoder is stateless.
var pgEncoder = encoder{}

// EncodeLiteral renders val as a PostgreSQL SQL literal. See encoder doc for
// the contract.
func (encoder) EncodeLiteral(val any, typeOID uint32) string {
	// Nil sentinel covers untyped nil; typed-nil interfaces fall through
	// to fmt.Sprintf in the default branch (acceptable: the user code that
	// produced the value is responsible for not lying about non-nil-ness).
	if val == nil {
		return "NULL"
	}

	// jsonb/json: only triggered by an explicit OID. Without it we cannot
	// distinguish raw JSON from a regular string.
	if typeOID == jsonbOID {
		if s, ok := val.(string); ok {
			return encodeEString(s) + "::jsonb"
		}
		if b, ok := val.([]byte); ok {
			return encodeEString(string(b)) + "::jsonb"
		}
	}
	if typeOID == jsonOID {
		if s, ok := val.(string); ok {
			return encodeEString(s) + "::json"
		}
		if b, ok := val.([]byte); ok {
			return encodeEString(string(b)) + "::json"
		}
	}

	switch v := val.(type) {
	case bool:
		if v {
			return "TRUE"
		}
		return "FALSE"

	case int:
		return strconv.FormatInt(int64(v), 10)
	case int8:
		return strconv.FormatInt(int64(v), 10)
	case int16:
		return strconv.FormatInt(int64(v), 10)
	case int32:
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	case uint:
		return strconv.FormatUint(uint64(v), 10)
	case uint8:
		return strconv.FormatUint(uint64(v), 10)
	case uint16:
		return strconv.FormatUint(uint64(v), 10)
	case uint32:
		return strconv.FormatUint(uint64(v), 10)
	case uint64:
		return strconv.FormatUint(v, 10)

	case float32:
		return encodeFloat(float64(v))
	case float64:
		return encodeFloat(v)

	case string:
		return encodeEString(v)

	case []byte:
		// Hex form: E'\x<hex>'::bytea. The leading backslash is doubled
		// when escaped as an E-string body (`\\x…`) so the SQL parser
		// sees the literal `\x…` it expects.
		return "E'\\\\x" + hex.EncodeToString(v) + "'::bytea"

	case time.Time:
		return "'" + v.UTC().Format(time.RFC3339Nano) + "'::timestamptz"

	case []any:
		return encodeAnySlice(v, typeOID)
	}

	// Typed slices: hand-roll the common element types so we recurse on
	// the right Go type without reflect. Nested slices fall through to
	// fallback (which still produces a safely-escaped literal — just one
	// that the server is unlikely to accept directly).
	switch v := val.(type) {
	case []bool:
		s := make([]any, len(v))
		for i := range v {
			s[i] = v[i]
		}
		return encodeAnySlice(s, typeOID)
	case []int:
		s := make([]any, len(v))
		for i := range v {
			s[i] = v[i]
		}
		return encodeAnySlice(s, typeOID)
	case []int32:
		s := make([]any, len(v))
		for i := range v {
			s[i] = v[i]
		}
		return encodeAnySlice(s, typeOID)
	case []int64:
		s := make([]any, len(v))
		for i := range v {
			s[i] = v[i]
		}
		return encodeAnySlice(s, typeOID)
	case []float64:
		s := make([]any, len(v))
		for i := range v {
			s[i] = v[i]
		}
		return encodeAnySlice(s, typeOID)
	case []string:
		s := make([]any, len(v))
		for i := range v {
			s[i] = v[i]
		}
		return encodeAnySlice(s, typeOID)
	}

	// Fallback: stringify then escape as an E-string. Critical for
	// SQL-injection safety — the input may carry quote characters that
	// would otherwise close the literal.
	return encodeEString(fmt.Sprintf("%v", val))
}

// encodeAnySlice renders an []any as PostgreSQL array syntax. Element NULLs
// (Go nil entries) are preserved verbatim. typeOID is propagated so jsonb
// arrays still trigger the right cast on elements.
func encodeAnySlice(v []any, typeOID uint32) string {
	parts := make([]string, len(v))
	for i, e := range v {
		parts[i] = pgEncoder.EncodeLiteral(e, typeOID)
	}
	return "ARRAY[" + strings.Join(parts, ", ") + "]"
}

// encodeFloat renders a float64 as a PostgreSQL numeric literal. Non-finite
// values use the explicit ::float8 cast form because bare NaN/Infinity
// tokens are not valid SQL.
func encodeFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return "'NaN'::float8"
	case math.IsInf(f, 1):
		return "'Infinity'::float8"
	case math.IsInf(f, -1):
		return "'-Infinity'::float8"
	}
	// 'g' with -1 precision yields the shortest round-tripping form, so
	// e.g. 0.1+0.2 encodes as "0.30000000000000004" — the exact float64
	// the runtime produced, which is what an INSERT-writer wants.
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// encodeEString wraps s as an E-string literal, doubling single quotes and
// backslashes. This is identical under both standard_conforming_strings=on
// and off — the E-prefix forces backslash-escape interpretation regardless.
func encodeEString(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 4)
	b.WriteString("E'")
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\'':
			b.WriteString("''")
		case '\\':
			b.WriteString("\\\\")
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('\'')
	return b.String()
}

// Compile-time assertion: encoder implements drivers.Encoder.
var _ drivers.Encoder = encoder{}
