-- +goose Up
-- Timelines: the second op-log document surface (M5). Same design as
-- canvases (0006): head_seq doubles as the append lock for gapless seqs.
CREATE TABLE timelines (
    id           text PRIMARY KEY,                        -- tl_*
    workspace_id text NOT NULL REFERENCES workspaces(id),
    project_id   text NOT NULL REFERENCES projects(id),
    doc_id       text NOT NULL UNIQUE REFERENCES docs(id),
    name         text NOT NULL,
    fps          int  NOT NULL DEFAULT 24,
    head_seq     bigint NOT NULL DEFAULT 0,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX timelines_project_idx ON timelines (project_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS timelines;
