-- describe_function — Postgres 17 catalog. Maps to models.FunctionDetail.
-- Returns ONE row per overload (pg_proc row) of (schema, name); schema + name
-- are bound as query params $1/$2 (NO identifier interpolation — mirrors
-- list_columns.sql). See pkg/drivers/pg/sql_test.go and DESIGN.md §11.1.
--
-- Args are aggregated into a single JSON array per overload so the row shape
-- stays one-row-per-overload. Each element is {name,type,mode}. proargmodes is
-- NULL when every argument is plain IN — in that case we fall back to
-- proargtypes (IN-only) and emit mode 'i'. When proargmodes IS present we use
-- proallargtypes (which includes OUT/INOUT/TABLE positions). proargnames is
-- NULL for fully-unnamed signatures and individual entries can be '' — both
-- surface as an empty Name in the model.
SELECT
    n.nspname AS schema,
    p.proname AS name,
    pg_get_function_result(p.oid) AS return_type,
    p.provolatile::text AS volatility,
    l.lanname AS language,
    COALESCE(
        (
            SELECT json_agg(
                json_build_object(
                    'name', COALESCE(an.argname, ''),
                    'type', format_type(at.argtype, NULL),
                    'mode', COALESCE(am.argmode, 'i')
                )
                ORDER BY at.ord
            )
            FROM unnest(COALESCE(p.proallargtypes, p.proargtypes::oid[]))
                 WITH ORDINALITY AS at(argtype, ord)
            LEFT JOIN unnest(p.proargnames)
                 WITH ORDINALITY AS an(argname, ord) ON an.ord = at.ord
            LEFT JOIN unnest(p.proargmodes)
                 WITH ORDINALITY AS am(argmode, ord) ON am.ord = at.ord
        ),
        '[]'::json
    ) AS args
FROM pg_proc p
JOIN pg_namespace n ON n.oid = p.pronamespace
JOIN pg_language l ON l.oid = p.prolang
WHERE n.nspname = $1
  AND p.proname = $2
  AND p.prokind = 'f'
ORDER BY p.oid
