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

func TestEmbeddedSQL_FilesystemHasExactlyTwelveSQLFiles(t *testing.T) {
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
		12,
		"expected exactly 12 embedded .sql files (got %v); add the new file's //go:embed directive in embed.go",
		sqlFiles,
	)
}
