-- Commenting on artifacts. Comments attach to a specific artifact version
-- (artifact_id is the per-version UUID), so they are inherently scoped to
-- the version they were made on. A thread carries an anchor (doc-level,
-- text-quote, or pin) plus an open/resolved status; comments hang off a
-- thread in time order. Hard-deleting an artifact cascades its comments.
--
-- +goose Up
CREATE TABLE comment_threads (
    thread_id   UUID PRIMARY KEY,
    artifact_id UUID NOT NULL REFERENCES artifacts(artifact_id) ON DELETE CASCADE,
    -- anchor: {"type":"doc"} | {"type":"text","quote":..,"elId":..} | {"type":"pin","elId":..,"rx":..,"ry":..}
    anchor      JSONB NOT NULL DEFAULT '{}'::jsonb,
    status      TEXT  NOT NULL DEFAULT 'open',  -- 'open' | 'resolved'
    created_by  TEXT  NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_by TEXT,
    resolved_at TIMESTAMPTZ
);
CREATE INDEX comment_threads_artifact_idx ON comment_threads (artifact_id);

CREATE TABLE comments (
    comment_id UUID PRIMARY KEY,
    thread_id  UUID NOT NULL REFERENCES comment_threads(thread_id) ON DELETE CASCADE,
    author     TEXT NOT NULL,
    body       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    edited_at  TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ
);
CREATE INDEX comments_thread_idx ON comments (thread_id, created_at);

-- +goose Down
DROP TABLE comments;
DROP TABLE comment_threads;
