-- list_columns — Postgres 17 catalog. Maps to models.Column. See pkg/drivers/pg/sql_test.go and DESIGN.md §11.3.
SELECT
    a.attname AS name,
    format_type(a.atttypid, a.atttypmod) AS data_type,
    pg_get_expr(ad.adbin, ad.adrelid) AS default_expr,
    NOT a.attnotnull AS nullable,
    a.attnum AS position,
    col_description(a.attrelid, a.attnum) AS description
FROM pg_attribute a
JOIN pg_class c ON c.oid = a.attrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_type t ON t.oid = a.atttypid
LEFT JOIN pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
WHERE n.nspname = $1
  AND c.relname = $2
  AND a.attnum > 0
  AND NOT a.attisdropped
ORDER BY a.attnum
