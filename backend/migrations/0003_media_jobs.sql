-- +goose Up
-- Media pipeline job queue: same SKIP LOCKED + NOTIFY semantics as
-- generation_jobs (TDD §3.1), separate table so media churn never contends
-- with generation claims.

CREATE TABLE media_jobs (
    id           text PRIMARY KEY,
    workspace_id text NOT NULL REFERENCES workspaces(id),
    kind         text NOT NULL,               -- probe | proxy | filmstrip | waveform (probe only in M1)
    input        jsonb NOT NULL,              -- {"version_id": "astv_..."}
    state        text NOT NULL DEFAULT 'queued', -- queued | running | complete | failed
    claimed_by   text,
    claimed_at   timestamptz,
    attempts     int NOT NULL DEFAULT 0,
    not_before   timestamptz NOT NULL DEFAULT now(),
    error        text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX media_jobs_claim_idx
    ON media_jobs (state, not_before, created_at)
    WHERE state = 'queued';

-- +goose Down
DROP TABLE IF EXISTS media_jobs;
