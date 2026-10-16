WITH namespace AS MATERIALIZED (
    SELECT n.oid, n.nspname::text AS name FROM pg_catalog.pg_namespace AS n
    WHERE n.nspname::text COLLATE "C" = $1::text COLLATE "C"
), page AS MATERIALIZED (
SELECT c.oid, n.name AS schema, c.relname::text AS name,
       CASE c.relkind WHEN 'r' THEN 'table' WHEN 'p' THEN 'partitioned_table'
           WHEN 'v' THEN 'view' WHEN 'm' THEN 'materialized_view' END AS kind
FROM pg_catalog.pg_class AS c JOIN namespace AS n ON n.oid = c.relnamespace
WHERE c.relname::text COLLATE "C" > $2::text COLLATE "C"
  AND c.relkind IN ('r', 'p', 'v', 'm')
ORDER BY c.relname::text COLLATE "C" LIMIT $3::integer
)
SELECT section, item FROM (
SELECT 0 AS section_order, 'schema'::text AS section, name,
       pg_catalog.jsonb_build_object('name', name) AS item FROM namespace
UNION ALL
SELECT 1, 'tables', name,
       pg_catalog.jsonb_build_object(
           'schema', schema, 'name', name, 'kind', kind,
           'comment', pg_catalog.obj_description(oid, 'pg_class')) FROM page
) AS facts ORDER BY section_order, name COLLATE "C"
