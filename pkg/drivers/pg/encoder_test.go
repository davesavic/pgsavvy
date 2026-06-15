package pg

import (
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/drivers"
)

// maliciousStringer's String() returns text that would close a naive string
// literal and append additional statements. The encoder MUST neutralize it
// by emitting an E-string whose content is the literal payload, escaped.
type maliciousStringer struct{}

func (maliciousStringer) String() string { return "'); DROP TABLE x;--" }

func TestEncoderEncodeLiteral(t *testing.T) {
	t.Parallel()

	var enc drivers.Encoder = encoder{}

	zeroTime := time.Time{}
	zeroTimeRendered := "'" + zeroTime.UTC().Format(time.RFC3339Nano) + "'::timestamptz"

	// 0.1+0.2 is documented because Go's runtime produces the IEEE-754
	// double 0.30000000000000004; strconv 'g' / -1 round-trips it
	// verbatim and that is what the INSERT-writer must emit.
	floatSum := 0.1 + 0.2
	floatSumRendered := strconv.FormatFloat(floatSum, 'g', -1, 64)

	cases := []struct {
		name    string
		val     any
		typeOID uint32
		want    string
	}{
		{name: "nil", val: nil, want: "NULL"},

		{name: "bool true", val: true, want: "TRUE"},
		{name: "bool false", val: false, want: "FALSE"},

		{name: "int", val: int(-42), want: "-42"},
		{name: "int64", val: int64(9_223_372_036_854_775_807), want: "9223372036854775807"},
		{name: "uint32", val: uint32(7), want: "7"},

		{name: "float finite", val: 1.5, want: "1.5"},
		{name: "float precision round-trip (0.1+0.2)", val: floatSum, want: floatSumRendered},
		{name: "float NaN", val: math.NaN(), want: "'NaN'::float8"},
		{name: "float +Inf", val: math.Inf(1), want: "'Infinity'::float8"},
		{name: "float -Inf", val: math.Inf(-1), want: "'-Infinity'::float8"},

		{name: "string plain", val: "hello", want: `E'hello'`},
		{name: "string with single quote", val: "O'Brien", want: `E'O''Brien'`},
		{name: "string with backslash", val: `c:\tmp\x`, want: `E'c:\\tmp\\x'`},
		{name: "string both quotes and backslash", val: `a'\b`, want: `E'a''\\b'`},
		{name: "string empty", val: "", want: `E''`},

		// E'\\x' is what Go's literal `E'\\x` looks like in the
		// resulting SQL — \\x is the escaped form the bytea parser
		// then reads as `\x`.
		{name: "bytea", val: []byte{0xDE, 0xAD, 0xBE, 0xEF}, want: `E'\\xdeadbeef'::bytea`},
		{name: "bytea empty", val: []byte{}, want: `E'\\x'::bytea`},

		{name: "time RFC3339Nano UTC", val: time.Date(2024, 1, 2, 3, 4, 5, 600, time.UTC), want: "'2024-01-02T03:04:05.0000006Z'::timestamptz"},
		{name: "time zero", val: zeroTime, want: zeroTimeRendered},

		{name: "jsonb via OID", val: `{"k":"v"}`, typeOID: jsonbOID, want: `E'{"k":"v"}'::jsonb`},
		{name: "json via OID", val: `[1,2,3]`, typeOID: jsonOID, want: `E'[1,2,3]'::json`},
		{name: "jsonb via OID with quote", val: `{"k":"O'B"}`, typeOID: jsonbOID, want: `E'{"k":"O''B"}'::jsonb`},

		{name: "array of int", val: []any{1, 2, 3}, want: "ARRAY[1, 2, 3]"},
		{name: "array preserves element NULL", val: []any{1, nil, 3}, want: "ARRAY[1, NULL, 3]"},
		{name: "array of string", val: []string{"a", "b'c"}, want: `ARRAY[E'a', E'b''c']`},
		{name: "array empty", val: []any{}, want: "ARRAY[]"},

		// Fallback path: unknown type — fmt.Sprintf("%v", v) yields the
		// String() return value, which is then E-string escaped. The
		// payload is therefore quoted, not executable.
		{name: "fallback malicious stringer", val: maliciousStringer{}, want: `E'''); DROP TABLE x;--'`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := enc.EncodeLiteral(tc.val, tc.typeOID)
			if got != tc.want {
				t.Fatalf("EncodeLiteral(%v, %d):\n got: %q\nwant: %q", tc.val, tc.typeOID, got, tc.want)
			}
		})
	}
}

// TestEncoderStandardConformingStringsInvariance documents the design
// guarantee: the encoder's output for strings/bytea uses the E'…' form
// unconditionally, which has identical semantics in both
// standard_conforming_strings=on and off. The test is therefore a
// static check that the produced literal starts with the E-prefix.
func TestEncoderStandardConformingStringsInvariance(t *testing.T) {
	t.Parallel()

	enc := encoder{}

	stringOut := enc.EncodeLiteral(`a\b'c`, 0)
	if !strings.HasPrefix(stringOut, "E'") {
		t.Fatalf("string encoding must use E-prefix form for stdconf invariance, got %q", stringOut)
	}

	byteaOut := enc.EncodeLiteral([]byte{0x01}, 0)
	if !strings.HasPrefix(byteaOut, "E'\\\\x") {
		t.Fatalf("bytea encoding must use E-prefix form for stdconf invariance, got %q", byteaOut)
	}
}

// TestEncoderFallbackEscapesSQLInjection asserts that any unrecognised type
// passes through the same E-string escape path. Anything an attacker can
// stuff into a value that satisfies fmt's %v cannot break out of the
// literal.
func TestEncoderFallbackEscapesSQLInjection(t *testing.T) {
	t.Parallel()

	enc := encoder{}
	out := enc.EncodeLiteral(maliciousStringer{}, 0)
	if strings.Contains(out, "DROP TABLE x;--") && !strings.Contains(out, "''") {
		t.Fatalf("malicious payload was not escaped: %q", out)
	}
	if !strings.HasPrefix(out, "E'") || !strings.HasSuffix(out, "'") {
		t.Fatalf("fallback output is not a properly delimited E-string: %q", out)
	}
}
