-- Per-version write-access list, sibling to allowed_access.
--
-- NULL (the default, and the state of every pre-existing row) means write
-- access FOLLOWS read access: anyone who can read the version can push the
-- next one — exactly the behavior before this column existed. A non-NULL
-- value (including the empty array) makes writes authoritative: only the
-- creator plus the listed tokens may version/append/edit. The empty array
-- therefore means creator-only writes.
--
-- Invariant enforced in the application on every write: allowed_write is a
-- SUBSET of allowed_access (write grants are unioned into allowed_access on
-- save), so every read path — SQL, Go, OpenSearch, comments — is untouched
-- by this column and needs no change.

-- +goose Up
ALTER TABLE artifacts ADD COLUMN allowed_write TEXT[];

-- +goose Down
ALTER TABLE artifacts DROP COLUMN allowed_write;
