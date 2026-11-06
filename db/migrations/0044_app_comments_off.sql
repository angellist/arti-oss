-- APPs now get the comments overlay when comments_enabled is TRUE, so every
-- existing APP document starts with it off and its owner opts in. A slug
-- counts as an APP when its newest version is one; all its versions change.

-- +goose Up
UPDATE artifacts SET comments_enabled = FALSE
WHERE (named_slug IS NULL AND artifact_type = 'APP')
   OR named_slug IN (
       SELECT latest.named_slug FROM (
           SELECT DISTINCT ON (named_slug) named_slug, artifact_type
           FROM artifacts
           WHERE named_slug IS NOT NULL
           ORDER BY named_slug, version DESC
       ) latest
       WHERE latest.artifact_type = 'APP'
   );

-- +goose Down
-- No-op: older code ignores the switch when serving APPs, and restoring TRUE
-- would re-open APPs whose owners had turned commenting off.
SELECT 1;
