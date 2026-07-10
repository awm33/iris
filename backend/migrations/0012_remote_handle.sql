-- +goose Up
-- Before-real-keys (PR 41): persist the adapter's remote pointer so a
-- reclaimed job RE-ATTACHES to its still-running remote task instead of
-- re-submitting a paid generation.
ALTER TABLE generation_jobs ADD COLUMN remote_handle jsonb;

-- +goose Down
ALTER TABLE generation_jobs DROP COLUMN remote_handle;
