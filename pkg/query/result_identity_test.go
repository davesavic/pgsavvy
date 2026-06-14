package query

import (
	"reflect"
	"testing"
)

func TestResultIdentity_DetectFromQuery(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want ResultIdentity
	}{
		// --- AC Rules: accept set --------------------------------
		{
			name: "rule_select_star_from_table",
			sql:  "SELECT * FROM users",
			want: ResultIdentity{BaseTable: "users", HasRowIdentity: true},
		},
		{
			name: "rule_select_col_list_from_table",
			sql:  "SELECT id, name, email FROM users",
			want: ResultIdentity{BaseTable: "users", HasRowIdentity: true},
		},
		{
			name: "rule_schema_qualified_table",
			sql:  "SELECT * FROM public.users",
			want: ResultIdentity{BaseTable: "public.users", HasRowIdentity: true},
		},
		{
			name: "rule_where_clause_allowed",
			sql:  "SELECT * FROM users WHERE id = 1",
			want: ResultIdentity{BaseTable: "users", HasRowIdentity: true},
		},
		{
			name: "rule_order_by_allowed",
			sql:  "SELECT * FROM users ORDER BY id",
			want: ResultIdentity{BaseTable: "users", HasRowIdentity: true},
		},
		{
			name: "rule_limit_allowed",
			sql:  "SELECT * FROM users LIMIT 10",
			want: ResultIdentity{BaseTable: "users", HasRowIdentity: true},
		},
		{
			name: "rule_trailing_semicolon",
			sql:  "SELECT * FROM users;",
			want: ResultIdentity{BaseTable: "users", HasRowIdentity: true},
		},

		// --- AC Rules: reject set --------------------------------
		{
			name: "reject_join",
			sql:  "SELECT * FROM users u JOIN orders o ON o.user_id = u.id",
			want: ResultIdentity{},
		},
		{
			name: "reject_cte",
			sql:  "WITH x AS (SELECT 1) SELECT * FROM x",
			want: ResultIdentity{},
		},
		{
			name: "reject_subquery_in_from",
			sql:  "SELECT * FROM (SELECT * FROM users) AS sub",
			want: ResultIdentity{},
		},
		{
			name: "reject_union",
			sql:  "SELECT * FROM users UNION SELECT * FROM admins",
			want: ResultIdentity{},
		},
		{
			name: "reject_intersect",
			sql:  "SELECT * FROM users INTERSECT SELECT * FROM admins",
			want: ResultIdentity{},
		},
		{
			name: "reject_except",
			sql:  "SELECT * FROM users EXCEPT SELECT * FROM admins",
			want: ResultIdentity{},
		},
		{
			name: "reject_aggregate_count",
			sql:  "SELECT COUNT(*) FROM users",
			want: ResultIdentity{},
		},
		{
			name: "reject_group_by",
			sql:  "SELECT name FROM users GROUP BY name",
			want: ResultIdentity{},
		},
		{
			name: "reject_set_returning_func_in_from",
			sql:  "SELECT * FROM generate_series(1, 10)",
			want: ResultIdentity{},
		},
		{
			name: "reject_function_call_in_select_list",
			sql:  "SELECT lower(name) FROM users",
			want: ResultIdentity{},
		},
		{
			name: "reject_distinct",
			sql:  "SELECT DISTINCT name FROM users",
			want: ResultIdentity{},
		},

		// --- AMENDMENTS / edge cases -----------------------------
		{
			name: "edge_malicious_string_with_double_dash",
			sql:  "SELECT * FROM users WHERE name = 'a--b'",
			want: ResultIdentity{BaseTable: "users", HasRowIdentity: true},
		},
		{
			name: "edge_string_literal_containing_FROM",
			sql:  "SELECT 'this has FROM in it' AS x FROM users",
			want: ResultIdentity{BaseTable: "users", HasRowIdentity: true},
		},
		{
			name: "edge_quoted_identifier_preserves_case",
			sql:  `SELECT * FROM "Users"`,
			want: ResultIdentity{BaseTable: "Users", HasRowIdentity: true},
		},
		{
			name: "edge_quoted_dotted_table_name",
			sql:  `SELECT * FROM "weird.name"`,
			want: ResultIdentity{BaseTable: "weird.name", HasRowIdentity: true},
		},
		{
			name: "edge_quoted_schema_and_table",
			sql:  `SELECT * FROM "MySchema"."MyTable"`,
			want: ResultIdentity{BaseTable: "MySchema.MyTable", HasRowIdentity: true},
		},
		{
			// The relationship panel's reverse-drill emits exactly this
			// quoted + predicated form (buildFKReverseSQL ->
			// QuoteQualified/QuoteIdent). It must resolve to the base table so
			// the panel can keep exploring the child rather than going blank.
			name: "edge_quoted_predicated_reverse_sql",
			sql:  `SELECT * FROM "app"."posts" WHERE "user_id"=$1`,
			want: ResultIdentity{BaseTable: "app.posts", HasRowIdentity: true},
		},
		{
			name: "edge_quoted_predicated_literal",
			sql:  `SELECT * FROM "app"."posts" WHERE "user_id"=1`,
			want: ResultIdentity{BaseTable: "app.posts", HasRowIdentity: true},
		},
		{
			name: "edge_unquoted_uppercase_normalised",
			sql:  "SELECT * FROM USERS",
			want: ResultIdentity{BaseTable: "users", HasRowIdentity: true},
		},
		{
			name: "edge_block_comment_stripped",
			sql:  "SELECT /* hint */ * FROM users",
			want: ResultIdentity{BaseTable: "users", HasRowIdentity: true},
		},
		{
			name: "edge_line_comment_stripped",
			sql:  "SELECT * FROM users -- trailing\n",
			want: ResultIdentity{BaseTable: "users", HasRowIdentity: true},
		},
		{
			name: "edge_line_comment_then_where",
			sql:  "SELECT * FROM users -- comment\nWHERE id = 1",
			want: ResultIdentity{BaseTable: "users", HasRowIdentity: true},
		},

		// --- Negative / boundary ---------------------------------
		{
			name: "neg_empty",
			sql:  "",
			want: ResultIdentity{},
		},
		{
			name: "neg_whitespace_only",
			sql:  "   \n\t",
			want: ResultIdentity{},
		},
		{
			name: "neg_not_a_select",
			sql:  "INSERT INTO users (id) VALUES (1)",
			want: ResultIdentity{},
		},
		{
			name: "neg_update",
			sql:  "UPDATE users SET name = 'x'",
			want: ResultIdentity{},
		},
		{
			name: "neg_multi_statement_one_rejected",
			sql:  "SELECT * FROM users; SELECT * FROM users u JOIN orders o ON o.user_id = u.id",
			want: ResultIdentity{},
		},
		{
			name: "neg_multi_statement_all_accepted_uses_first",
			sql:  "SELECT * FROM users; SELECT * FROM admins",
			want: ResultIdentity{BaseTable: "users", HasRowIdentity: true},
		},
		{
			name: "neg_trailing_having",
			sql:  "SELECT * FROM users HAVING id > 1",
			want: ResultIdentity{},
		},
		{
			name: "neg_window_function",
			sql:  "SELECT id, row_number() OVER (ORDER BY id) FROM users",
			want: ResultIdentity{},
		},
		{
			name: "neg_select_constant_no_from",
			sql:  "SELECT 1",
			want: ResultIdentity{},
		},
		{
			name: "neg_semicolon_in_string",
			sql:  "SELECT * FROM users WHERE name = 'a;b'",
			want: ResultIdentity{BaseTable: "users", HasRowIdentity: true},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DetectFromQuery(tc.sql)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("DetectFromQuery(%q)\n  got  = %+v\n  want = %+v", tc.sql, got, tc.want)
			}
		})
	}
}
