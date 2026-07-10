-- +goose Up
-- Export service v1 (M7): server renders of a timeline. One row per
-- requested render; the work rides media_jobs (kind=export) and the result
-- lands as a normal library asset so download/poster/lineage all reuse the
-- asset machinery.
CREATE TABLE exports (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces (id),
    project_id   TEXT NOT NULL REFERENCES projects (id),
    timeline_id  TEXT NOT NULL REFERENCES timelines (id) ON DELETE CASCADE,
    preset       TEXT NOT NULL,
    state        TEXT NOT NULL DEFAULT 'queued'
                 CHECK (state IN ('queued', 'running', 'complete', 'failed')),
    error        TEXT NOT NULL DEFAULT '',
    asset_id     TEXT NOT NULL DEFAULT '',
    version_id   TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX exports_timeline_idx ON exports (timeline_id, created_at DESC);

-- +goose Down
DROP TABLE exports;
