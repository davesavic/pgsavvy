-- table_stats — Postgres 17 catalog. Populates models.Table.EstimatedRows and SizeBytes. See pkg/drivers/pg/sql_test.go and DESIGN.md §11.3.
SELECT
    s.schema,
    s.name,
    c.reltuples::bigint AS estimated_rows,
    pg_total_relation_size(c.oid)::bigint AS size_bytes
FROM unnest($1::text[], $2::text[]) WITH ORDINALITY AS s(schema, name, ord)
JOIN pg_namespace n ON n.nspname = s.schema
JOIN pg_class c ON c.relnamespace = n.oid AND c.relname = s.name
ORDER BY s.ord
