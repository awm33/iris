-- +goose Up
-- Canvases: the image-studio document surface (M4). The op log itself lives
-- in the existing docs/doc_ops tables (0001); this table is the canvas-kind
-- metadata plus head_seq, which doubles as the append lock — AppendOps does
-- a conditional UPDATE on head_seq (optimistic concurrency, single writer
-- per doc) and inserts ops at base+1..base+n, keeping seqs gapless.

CREATE TABLE canvases (
    id           text PRIMARY KEY,                        -- cnv_*
    workspace_id text NOT NULL REFERENCES workspaces(id),
    project_id   text NOT NULL REFERENCES projects(id),
    doc_id       text NOT NULL UNIQUE REFERENCES docs(id),
    name         text NOT NULL,
    width        int  NOT NULL,
    height       int  NOT NULL,
    head_seq     bigint NOT NULL DEFAULT 0,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX canvases_project_idx ON canvases (project_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS canvases;
