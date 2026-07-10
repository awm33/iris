-- +goose Up
-- Transcriptions (M7): one row per transcribe request over a timeline's
-- mixed audio. The work rides media_jobs (kind=transcribe); the RESULT is
-- caption ops appended to the timeline doc — this row only carries
-- lifecycle for the UI (exports' pattern).
CREATE TABLE transcriptions (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces (id),
    project_id   TEXT NOT NULL REFERENCES projects (id),
    timeline_id  TEXT NOT NULL REFERENCES timelines (id) ON DELETE CASCADE,
    state        TEXT NOT NULL DEFAULT 'queued'
                 CHECK (state IN ('queued', 'running', 'complete', 'failed')),
    error        TEXT NOT NULL DEFAULT '',
    segment_count INTEGER NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX transcriptions_timeline_idx ON transcriptions (timeline_id, created_at DESC);

-- +goose Down
DROP TABLE transcriptions;
