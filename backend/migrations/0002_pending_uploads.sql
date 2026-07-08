-- +goose Up
-- Staging metadata for the two-phase upload flow (StartUpload → client PUT →
-- CompleteUpload). Rows are deleted on completion; stale rows are garbage.

CREATE TABLE pending_uploads (
    id           text PRIMARY KEY,
    workspace_id text NOT NULL REFERENCES workspaces(id),
    project_id   text REFERENCES projects(id),
    filename     text NOT NULL,
    content_type text NOT NULL,
    size_bytes   bigint NOT NULL,
    object_key   text NOT NULL,  -- temp key; moved to sha256/<hash> on complete
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS pending_uploads;
