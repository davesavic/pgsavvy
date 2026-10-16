WITH target AS MATERIALIZED (
    SELECT c.oid, n.nspname::text AS schema, c.relname::text AS name,
           CASE c.relkind WHEN 'r' THEN 'table' WHEN 'p' THEN 'partitioned_table'
                WHEN 'v' THEN 'view' WHEN 'm' THEN 'materialized_view' END AS kind,
           pg_catalog.obj_description(c.oid, 'pg_class') AS comment
    FROM pg_catalog.pg_class AS c JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
    WHERE n.nspname::text COLLATE "C" = $1::text COLLATE "C"
      AND c.relname::text COLLATE "C" = $2::text COLLATE "C"
      AND c.relkind IN ('r','p','v','m')
), outbound_source AS MATERIALIZED (
    SELECT c.* FROM target JOIN pg_catalog.pg_constraint AS c ON c.conrelid = target.oid
    WHERE c.contype = 'f' ORDER BY c.conname::text COLLATE "C", c.oid LIMIT 1001
), inbound_source AS MATERIALIZED (
    SELECT c.* FROM target JOIN pg_catalog.pg_constraint AS c ON c.confrelid = target.oid
    WHERE c.contype = 'f' ORDER BY c.conname::text COLLATE "C", c.oid LIMIT 1001
), directional AS (
    SELECT 'outbound'::text AS direction, * FROM outbound_source
    UNION ALL SELECT 'inbound'::text, * FROM inbound_source
)
SELECT section, item FROM (
    SELECT 0 AS section_order, 'relation'::text AS section, name,
           pg_catalog.jsonb_build_object('schema', schema, 'name', name, 'kind', kind, 'comment', comment) AS item
    FROM target
    UNION ALL
    SELECT 1, d.direction, d.conname::text,
           pg_catalog.jsonb_build_object('name', d.conname::text,
               'schema', sn.nspname::text, 'table', s.relname::text,
               'columns', ARRAY(SELECT a.attname::text FROM pg_catalog.unnest(d.conkey) WITH ORDINALITY AS k(attnum, ord)
                   JOIN pg_catalog.pg_attribute AS a ON a.attrelid = d.conrelid AND a.attnum = k.attnum ORDER BY k.ord LIMIT 1001),
               'ref_schema', tn.nspname::text, 'ref_table', t.relname::text,
               'ref_columns', ARRAY(SELECT a.attname::text FROM pg_catalog.unnest(d.confkey) WITH ORDINALITY AS k(attnum, ord)
                   JOIN pg_catalog.pg_attribute AS a ON a.attrelid = d.confrelid AND a.attnum = k.attnum ORDER BY k.ord LIMIT 1001),
               'on_update', CASE d.confupdtype WHEN 'a' THEN 'NO ACTION' WHEN 'r' THEN 'RESTRICT' WHEN 'c' THEN 'CASCADE' WHEN 'n' THEN 'SET NULL' WHEN 'd' THEN 'SET DEFAULT' END,
               'on_delete', CASE d.confdeltype WHEN 'a' THEN 'NO ACTION' WHEN 'r' THEN 'RESTRICT' WHEN 'c' THEN 'CASCADE' WHEN 'n' THEN 'SET NULL' WHEN 'd' THEN 'SET DEFAULT' END)
    FROM directional AS d
    JOIN pg_catalog.pg_class AS s ON s.oid = d.conrelid JOIN pg_catalog.pg_namespace AS sn ON sn.oid = s.relnamespace
    JOIN pg_catalog.pg_class AS t ON t.oid = d.confrelid JOIN pg_catalog.pg_namespace AS tn ON tn.oid = t.relnamespace
) AS facts
ORDER BY section_order, section COLLATE "C", name COLLATE "C"
