package pg

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEmbeddedSQL_ContainsExpectedIdentifiers(t *testing.T) {
	cases := []struct {
		name     string
		query    string
		contains []string
	}{
		{
			name:  "list_databases",
			query: sqlListDatabases,
			contains: []string{
				"pg_database",
				"template0",
				"template1",
			},
		},
		{
			name:  "list_schemas",
			query: sqlListSchemas,
			contains: []string{
				"pg_namespace",
			},
		},
		{
			name:  "list_tables",
			query: sqlListTables,
			contains: []string{
				"pg_class",
				"relkind",
				"'r'",
				"'m'",
				"'v'",
				"'p'",
			},
		},
		{
			name:  "table_stats",
			query: sqlTableStats,
			contains: []string{
				"pg_total_relation_size",
				"reltuples",
				"unnest",
			},
		},
		{
			name:  "list_columns",
			query: sqlListColumns,
			contains: []string{
				"pg_attribute",
				"pg_attrdef",
				"$1",
				"$2",
				"attisdropped",
				"attnum",
			},
		},
		{
			name:  "list_indexes",
			query: sqlListIndexes,
			contains: []string{
				"pg_index",
				"with ordinality",
				"$1",
				"$2",
			},
		},
		{
			name:  "list_constraints",
			query: sqlListConstraints,
			contains: []string{
				"pg_constraint",
				"pg_get_constraintdef",
				"$1",
				"$2",
			},
		},
		{
			name:  "list_foreign_keys",
			query: sqlListForeignKeys,
			contains: []string{
				"pg_constraint",
				"confkey",
				"conkey",
				"with ordinality",
				"'f'",
				"$1",
				"$2",
			},
		},
		{
			name:  "list_inbound_foreign_keys",
			query: sqlListInboundForeignKeys,
			contains: []string{
				"pg_constraint",
				"confkey",
				"conkey",
				"with ordinality",
				"'f'",
				"$1",
				"$2",
				"rn.nspname",
				"rc.relname",
			},
		},
		{
			name:  "list_functions",
			query: sqlListFunctions,
			contains: []string{
				"information_schema.routines",
				"routine_name",
				"current_schemas",
				"'FUNCTION'",
			},
		},
		{
			name:  "describe_function",
			query: sqlDescribeFunction,
			contains: []string{
				"pg_proc",
				"pg_namespace",
				"pg_language",
				"pg_get_function_result",
				"provolatile",
				"proargmodes",
				"$1",
				"$2",
			},
		},
		{
			name:  "editability_introspect",
			query: sqlEditabilityIntrospect,
			contains: []string{
				"pg_class",
				"pg_index",
				"pg_namespace",
				"$1",
				"$2",
				"indisprimary",
				"indisunique",
			},
		},
		{
			name:  "table_names_by_oid",
			query: sqlTableNamesByOID,
			contains: []string{
				"pg_class",
				"relname",
				"$1",
				"oid[]",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.NotEmpty(t, tc.query, "embedded query must not be empty")
			lower := strings.ToLower(tc.query)
			for _, needle := range tc.contains {
				require.Contains(
					t,
					lower,
					strings.ToLower(needle),
					"query %q missing expected identifier %q",
					tc.name,
					needle,
				)
			}
		})
	}
}

func TestEmbeddedSQL_FilesystemHasExactlyThirteenSQLFiles(t *testing.T) {
	entries, err := sqlFS.ReadDir("sql")
	require.NoError(t, err)

	var sqlFiles []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".sql") {
			sqlFiles = append(sqlFiles, e.Name())
		}
	}

	require.Len(
		t,
		sqlFiles,
		13,
		"expected exactly 13 embedded .sql files (got %v); add the new file's //go:embed directive in embed.go",
		sqlFiles,
	)
}

// TestDescribeFunctionSQLIsParameterized is the SQLi-mandate guard: schema and
// function name MUST be bound as $1/$2 (mirrors list_columns.sql), never
// string-interpolated. A function name containing a single quote is therefore
// inert — it can only ever arrive as a bound parameter value, so this asserts
// the query has no string-concatenation seam an attacker could exploit.
func TestDescribeFunctionSQLIsParameterized(t *testing.T) {
	const maliciousName = "foo'); DROP TABLE app.users; --"
	require.Contains(t, sqlDescribeFunction, "$1", "schema must be a bound param")
	require.Contains(t, sqlDescribeFunction, "$2", "name must be a bound param")
	// The embedded SQL must not embed the attacker-controlled value: it is a
	// static string with no fmt verbs or the malicious literal.
	require.NotContains(t, sqlDescribeFunction, maliciousName)
	require.NotContains(t, sqlDescribeFunction, "%s", "no printf interpolation seam")
}
