-- +goose Up
ALTER TABLE artifacts ADD COLUMN scopes TEXT[] NOT NULL DEFAULT '{}';

-- Backfill from the scalar `scope` column. Existing values arrive in several
-- shapes, each stored as a single TEXT string:
--   NULL / ''                       -> {}
--   scopex            (bare)        -> {scopex}
--   topic:foo:bar     (bare+colons) -> {topic:foo:bar}
--   "scopex"          (JSON string) -> {scopex}
--   ["scopex"]        (JSON array)  -> {scopex}
--   ["scopex","y"]    (JSON array)  -> {scopex, y}
--
-- A session-local (pg_temp) parser handles each shape and NEVER aborts: any
-- value that isn't valid JSON (a bare scope, or a malformed "[oops") is kept
-- verbatim as a single scope rather than erroring the whole migration.
-- Elements are trimmed and empty elements dropped. pg_temp.* is dropped
-- automatically when goose's session ends — no cleanup needed.
-- +goose StatementBegin
CREATE FUNCTION pg_temp.parse_scopes(s text) RETURNS text[] AS $fn$
DECLARE
    j   jsonb;
    raw text[];
BEGIN
    IF s IS NULL OR btrim(s) = '' THEN
        RETURN '{}';
    END IF;
    -- Try to read the value as JSON; anything that doesn't parse is a bare scope.
    BEGIN
        j := btrim(s)::jsonb;
    EXCEPTION WHEN others THEN
        j := NULL;
    END;
    IF j IS NULL THEN
        raw := ARRAY[s];
    ELSIF jsonb_typeof(j) = 'array' THEN
        raw := ARRAY(SELECT e FROM jsonb_array_elements_text(j) WITH ORDINALITY t(e, ord) ORDER BY ord);
    ELSIF jsonb_typeof(j) = 'string' THEN
        raw := ARRAY[j #>> '{}'];
    ELSE
        -- number / boolean / object: not a meaningful scope shape, keep raw text.
        raw := ARRAY[s];
    END IF;
    RETURN ARRAY(
        SELECT btrim(e) FROM unnest(raw) WITH ORDINALITY u(e, ord)
        WHERE btrim(e) <> '' ORDER BY ord
    );
END;
$fn$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- +goose StatementBegin
UPDATE artifacts SET scopes = pg_temp.parse_scopes(scope)
WHERE scope IS NOT NULL AND btrim(scope) <> '';
-- +goose StatementEnd

CREATE INDEX ix_artifacts_scopes ON artifacts USING GIN (scopes);

-- +goose Down
DROP INDEX IF EXISTS ix_artifacts_scopes;
ALTER TABLE artifacts DROP COLUMN IF EXISTS scopes;
