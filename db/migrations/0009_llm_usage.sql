-- Usage ledger for the built-in `llm.complete` completion (Auth:"service").
-- Append-only; doubles as the source of truth for the per-viewer / per-app /
-- per-org budget gate (counting from the shared table is what keeps the caps
-- correct across all arti pods). Stores token counts + request id, NOT prompt
-- or response text.
--
-- +goose Up
CREATE TABLE llm_usage (
  id            BIGSERIAL PRIMARY KEY,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  viewer_email  TEXT NOT NULL,           -- from the scoped app-token
  app_id        TEXT NOT NULL,           -- artifact id
  model         TEXT NOT NULL,
  input_tokens  INT NOT NULL,
  output_tokens INT NOT NULL,
  cost_micros   BIGINT NOT NULL,         -- millionths of a USD, from per-model $/MTok
  request_id    TEXT,                    -- Anthropic message id, for support
  ok            BOOLEAN NOT NULL
);
CREATE INDEX llm_usage_viewer_idx ON llm_usage (viewer_email, created_at);
CREATE INDEX llm_usage_app_idx ON llm_usage (app_id, created_at);

-- +goose Down
DROP TABLE llm_usage;
