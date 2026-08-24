-- Anchor a slug-scoped share link to the lineage it was minted against.
--
-- An all-archived slug is free for anyone to reuse (see the comment at
-- internal/store/pgstore/store.go:355 — deliberate, pre-existing behaviour),
-- and a tracking link stores only the bare slug string. Without an anchor,
-- this sequence served one user's private document to the internet:
--
--   1. A publishes to slug S and mints a slug-scoped share link.
--   2. A archives every version of S, freeing the slug.
--   3. B publishes a PRIVATE document to S.
--   4. A's unexpired link resolves S's newest row — B's document.
--
-- anchor_owner records who owned the slug's LIVE lineage when the link was
-- minted. Resolution compares it against who owns that lineage now, and
-- refuses on a mismatch.
--
-- The column defaults to empty and is NOT backfilled, so a slug-scoped row
-- created before this migration carries no anchor. Resolution refuses those
-- rather than skipping the check for them: an unanchored slug link is exactly
-- the one that can follow a reclaimed slug to whoever occupies it next, which
-- is the bypass this column exists to close. Such links are re-mintable.
--
-- Pinned links are unaffected and always have an empty anchor: they carry a
-- NULL slug, name an artifact_id directly, and cannot follow a slug anywhere.

-- +goose Up
ALTER TABLE share_links ADD COLUMN IF NOT EXISTS anchor_owner TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE share_links DROP COLUMN IF EXISTS anchor_owner;
