-- A person from an accepted-but-not-auto-admitted domain asks for an account
-- here; an admin approves (which adds a users row) or declines. One row per
-- email: a decision is final, and a declined person is added by hand if needed.

-- +goose Up
CREATE TABLE access_requests (
    email        TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'PENDING'
                     CHECK (status IN ('PENDING', 'APPROVED', 'DENIED')),
    requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_by   TEXT,
    decided_at   TIMESTAMPTZ
);

-- +goose Down
DROP TABLE access_requests;
