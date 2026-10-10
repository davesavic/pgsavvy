-- editability_introspect — Postgres 17 catalog. F2.
--
-- For each distinct pg_class OID supplied in $1, return:
--   oid           — the input OID (so callers can correlate without reordering)
--   relkind       — pg_class.relkind (r/v/m/f/p/t etc.)
--   schemaname    — pg_namespace.nspname
--   relname       — pg_class.relname
--   is_temp_schema— pg_namespace.nspname starts with 'pg_temp'
--   pk_attnums    — attnums covered by the relation's primary key (NULL when none)
--   unique_attnums-- JSON array of independent UNIQUE key arrays, NULL when none
--
-- $2 is the SELECT-order TableAttributeNumber array (smallint[]). It is
-- currently unused server-side — the Go layer correlates it against the
-- returned PK/UNIQUE attnum sets to compute RowIdentity — but accepting it
-- here keeps the wire shape stable for any future server-side correlation.

SELECT
    c.oid                                              AS oid,
    c.relkind::text                                    AS relkind,
    n.nspname                                          AS schemaname,
    c.relname                                          AS relname,
    (n.nspname LIKE 'pg_temp%')                        AS is_temp_schema,
    (
        SELECT array_agg(k::int ORDER BY ord)
        FROM pg_index i
        CROSS JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS u(k, ord)
        WHERE i.indrelid = c.oid
          AND i.indisprimary
          AND ord <= i.indnkeyatts
          AND k > 0
    )                                                  AS pk_attnums,
    (
        SELECT jsonb_agg(to_jsonb(per_index) ORDER BY indexrelid)
        FROM (
            SELECT
                i.indexrelid,
                (
                    SELECT array_agg(k::int ORDER BY ord)
                    FROM unnest(i.indkey) WITH ORDINALITY AS u(k, ord)
                    WHERE ord <= i.indnkeyatts
                      AND k > 0
                ) AS per_index
            FROM pg_index i
            WHERE i.indrelid = c.oid
              AND i.indisunique
              AND i.indpred IS NULL
              AND i.indexprs IS NULL
              AND NOT i.indisprimary
              AND i.indisvalid
              AND i.indisready
        ) s
        WHERE per_index IS NOT NULL
    )                                                  AS unique_attnums,
    (
        SELECT jsonb_object_agg(a.attnum, a.attname)
        FROM pg_attribute a
        WHERE a.attrelid = c.oid
          AND a.attnum > 0
          AND NOT a.attisdropped
    )                                                  AS column_names
FROM unnest($1::oid[]) AS req(oid)
JOIN pg_class c ON c.oid = req.oid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE COALESCE(cardinality($2::smallint[]), 0) >= 0;
