WITH target AS MATERIALIZED (
    SELECT c.oid, n.nspname::text AS schema, c.relname::text AS name,
           CASE c.relkind WHEN 'r' THEN 'table' WHEN 'p' THEN 'partitioned_table'
                WHEN 'v' THEN 'view' WHEN 'm' THEN 'materialized_view' END AS kind,
           pg_catalog.obj_description(c.oid, 'pg_class') AS comment
    FROM pg_catalog.pg_class AS c
    JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
    WHERE n.nspname::text COLLATE "C" = $1::text COLLATE "C"
      AND c.relname::text COLLATE "C" = $2::text COLLATE "C"
      AND c.relkind IN ('r','p','v','m')
), columns_source AS MATERIALIZED (
    SELECT a.attname::text AS name, a.attnum::integer AS position,
           pg_catalog.format_type(a.atttypid, a.atttypmod) AS data_type,
           NOT (a.attnotnull OR (t.typtype = 'd' AND t.typnotnull)) AS nullable,
           CASE WHEN a.attgenerated = '' THEN pg_catalog.pg_get_expr(d.adbin, d.adrelid) END AS "default",
           pg_catalog.col_description(a.attrelid, a.attnum) AS comment
    FROM target
    JOIN pg_catalog.pg_attribute AS a ON a.attrelid = target.oid
    JOIN pg_catalog.pg_type AS t ON t.oid = a.atttypid
    LEFT JOIN pg_catalog.pg_attrdef AS d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
    WHERE a.attnum > 0 AND NOT a.attisdropped
    ORDER BY a.attnum LIMIT 1001
), constraints_source AS MATERIALIZED (
    SELECT c.oid, c.conname::text AS name, c.contype::text AS kind, c.conrelid, c.conkey
    FROM target JOIN pg_catalog.pg_constraint AS c ON c.conrelid = target.oid
    ORDER BY c.conname::text COLLATE "C", c.oid LIMIT 1001
), indexes_source AS MATERIALIZED (
    SELECT i.indexrelid, c.relname::text AS name, i.indisunique AS "unique",
           i.indisprimary AS "primary", am.amname::text AS method
    FROM target JOIN pg_catalog.pg_index AS i ON i.indrelid = target.oid
    JOIN pg_catalog.pg_class AS c ON c.oid = i.indexrelid
    JOIN pg_catalog.pg_am AS am ON am.oid = c.relam
    ORDER BY c.relname::text COLLATE "C" LIMIT 1001
)
SELECT section, item FROM (
    SELECT 0 AS section_order, 'relation'::text AS section, 0 AS ordinal, name,
           pg_catalog.jsonb_build_object('schema', schema, 'name', name, 'kind', kind, 'comment', comment) AS item
    FROM target
    UNION ALL
    SELECT 1, 'columns', position, name, pg_catalog.to_jsonb(columns_source) FROM columns_source
    UNION ALL
    SELECT 2, 'constraints', 0, name,
           pg_catalog.jsonb_build_object('name', name, 'kind', kind,
               'definition', pg_catalog.pg_get_constraintdef(oid, true),
               'columns', ARRAY(SELECT a.attname::text FROM pg_catalog.unnest(conkey) WITH ORDINALITY AS k(attnum, ord)
                   JOIN pg_catalog.pg_attribute AS a ON a.attrelid = conrelid AND a.attnum = k.attnum
                   ORDER BY k.ord LIMIT 1001))
    FROM constraints_source
    UNION ALL
    SELECT 3, 'indexes', 0, name,
           pg_catalog.jsonb_build_object('name', name, 'unique', "unique", 'primary', "primary", 'method', method,
               'definition', pg_catalog.pg_get_indexdef(indexrelid))
    FROM indexes_source
) AS facts
ORDER BY section_order, ordinal, name COLLATE "C"
