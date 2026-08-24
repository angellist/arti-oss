-- A sparse roster record for principals arti cannot otherwise name.
--
-- Numbered 0023 rather than 0022: PR #257 (external share links) already claims
-- 0022 on an open branch. main is at 0021, so both are individually valid and
-- one of them had to move; this one did, because it was the later of the two to
-- open. A gap at 0022 is harmless if #257 is abandoned — goose tracks applied
-- version ids, not a contiguous sequence.
--
-- arti has no account provisioning: it trusts the identity its auth layer hands
-- it, and the set of people it knows is derived from the tables that happen to
-- have recorded an email (see pgstore.knownPrincipalsCTE). That derivation
-- covers everyone who has signed in, created something, been granted a role or
-- been put in a group.
--
-- What it cannot express is a principal at BASELINE privilege that has done
-- none of those things yet — a service account someone wants recorded before
-- its first use. A role assignment can't say it either: AssignRole rejects the
-- USER role outright, because USER is the implicit baseline everyone holds. So
-- this table is the only place such a principal can be written down.
--
-- Deliberately minimal, and deliberately NOT authoritative:
--
--   * No last-seen column. That stays user_idp_groups.captured_at, so there is
--     exactly one record of when someone last signed in.
--   * No login-path write. Anyone who signs in is already derivable, so this
--     table has nothing to add for them.
--   * No backfill. The derivation already covers every principal that exists,
--     so this starts empty and only ever holds rows a person added.
--   * No foreign keys from the other email-bearing columns, and no read of this
--     table in any authorization decision. A missing row must deny nothing —
--     making membership here a gate would turn a bookkeeping omission into a
--     lockout.
--
-- +goose Up
CREATE TABLE users (
    email    TEXT PRIMARY KEY,                                   -- lowercased
    kind     TEXT        NOT NULL DEFAULT 'human'
             CHECK (kind IN ('human', 'service')),
    note     TEXT        NOT NULL DEFAULT '',                    -- e.g. owning team
    added_by TEXT        NOT NULL DEFAULT '',                    -- who recorded it
    added_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE users;
