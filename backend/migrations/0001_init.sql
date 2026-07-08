-- +goose Up
-- Iris core schema v0 (TDD §3.2). IDs are type-prefixed ULIDs (text).
-- Workspace scoping on every row; RLS added as defense-in-depth in a later migration.

CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE workspaces (
    id          text PRIMARY KEY,
    name        text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE members (
    id           text PRIMARY KEY,
    workspace_id text NOT NULL REFERENCES workspaces(id),
    email        text NOT NULL,
    role         text NOT NULL DEFAULT 'owner', -- owner | editor | viewer (future)
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, email)
);

CREATE TABLE projects (
    id           text PRIMARY KEY,
    workspace_id text NOT NULL REFERENCES workspaces(id),
    name         text NOT NULL,
    description  text NOT NULL DEFAULT '',
    archived_at  timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX projects_workspace_idx ON projects (workspace_id) WHERE archived_at IS NULL;

-- ── Assets: mutable identity + immutable versions ─────────────────────────────

CREATE TABLE assets (
    id              text PRIMARY KEY,
    workspace_id    text NOT NULL REFERENCES workspaces(id),
    project_id      text REFERENCES projects(id), -- NULL = workspace-level (shared)
    kind            text NOT NULL,                -- image | video | audio | model_3d | lut | font
    name            text NOT NULL,
    head_version_id text,                         -- FK added after asset_versions exists
    tags            text[] NOT NULL DEFAULT '{}',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX assets_project_idx ON assets (project_id, kind);
CREATE INDEX assets_workspace_idx ON assets (workspace_id, kind);

CREATE TABLE asset_versions (
    id           text PRIMARY KEY,
    asset_id     text NOT NULL REFERENCES assets(id),
    sha256       text NOT NULL,       -- content-addressed object key: sha256/<hash>
    content_type text NOT NULL,
    size_bytes   bigint NOT NULL,
    width        int,
    height       int,
    duration_s   double precision,
    fps          double precision,
    meta         jsonb NOT NULL DEFAULT '{}',  -- probe output, color info, etc.
    embedding    vector(768),                  -- CLIP-class, filled by media pipeline
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX asset_versions_asset_idx ON asset_versions (asset_id, created_at DESC);
CREATE INDEX asset_versions_sha_idx ON asset_versions (sha256);

ALTER TABLE assets
    ADD CONSTRAINT assets_head_version_fk
    FOREIGN KEY (head_version_id) REFERENCES asset_versions(id) DEFERRABLE INITIALLY DEFERRED;

-- Lineage: first-class edges (TDD §3.2). Read by the lineage UI and stale propagation.
CREATE TABLE asset_links (
    from_version_id text NOT NULL REFERENCES asset_versions(id),
    to_entity_id    text NOT NULL,  -- job_ | tk_ | ast_ | ...
    role            text NOT NULL,  -- generated_by | reference_of | conditioning_frame_of | derived_from | used_in_take
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (from_version_id, to_entity_id, role)
);
CREATE INDEX asset_links_to_idx ON asset_links (to_entity_id);

-- ── Story domain (filled out in M3; tables reserved now for FK stability) ─────

CREATE TABLE scenes (
    id          text PRIMARY KEY,
    project_id  text NOT NULL REFERENCES projects(id),
    name        text NOT NULL,
    position    int  NOT NULL DEFAULT 0,
    style_notes text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sets (
    id           text PRIMARY KEY,
    scene_id     text NOT NULL UNIQUE REFERENCES scenes(id),
    model3d_ref  jsonb,  -- {asset_id, version_id|null}
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE views (
    id          text PRIMARY KEY,
    set_id      text NOT NULL REFERENCES sets(id),
    name        text NOT NULL,
    plate_ref   jsonb NOT NULL,  -- {asset_id, version_id|null}
    camera      jsonb,           -- registration against the 3D set, nullable
    position    int NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE characters (
    id           text PRIMARY KEY,
    workspace_id text NOT NULL REFERENCES workspaces(id),
    project_id   text REFERENCES projects(id), -- NULL = workspace-level
    name         text NOT NULL,
    refs         jsonb NOT NULL DEFAULT '[]',  -- [{role: turnaround|expression|voice, asset_id, version_id|null}]
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE shots (
    id          text PRIMARY KEY,
    scene_id    text NOT NULL REFERENCES scenes(id),
    position    int NOT NULL DEFAULT 0,
    description text NOT NULL DEFAULT '',
    duration_target_s double precision,
    view_id     text REFERENCES views(id),
    cast_ids    text[] NOT NULL DEFAULT '{}',
    selected_take_id text, -- FK deferred (takes below)
    continuity_stale boolean NOT NULL DEFAULT false,
    pinned      boolean NOT NULL DEFAULT false, -- opt out of stale cascades
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE takes (
    id            text PRIMARY KEY,
    shot_id       text NOT NULL REFERENCES shots(id),
    job_id        text,           -- generation job that produced it (NULL = imported)
    version_id    text REFERENCES asset_versions(id), -- the video artifact
    quality       text NOT NULL DEFAULT 'draft',      -- draft | master
    parent_take_id text REFERENCES takes(id),
    recipe        jsonb NOT NULL DEFAULT '{}',        -- full provenance: model, prompt, seed, refs, conditioning, params
    starred       boolean NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX takes_shot_idx ON takes (shot_id, created_at DESC);

ALTER TABLE shots
    ADD CONSTRAINT shots_selected_take_fk
    FOREIGN KEY (selected_take_id) REFERENCES takes(id) DEFERRABLE INITIALLY DEFERRED;

-- Continuity edges (TDD §3.2): prev → next with carried context.
CREATE TABLE sequence_edges (
    prev_shot_id text NOT NULL REFERENCES shots(id),
    next_shot_id text NOT NULL REFERENCES shots(id),
    carry        jsonb NOT NULL DEFAULT '{"last_frame": true}', -- {last_frame, refs[], style, camera}
    PRIMARY KEY (prev_shot_id, next_shot_id)
);

-- ── Model endpoints & generation jobs ──────────────────────────────────────────

CREATE TABLE model_endpoints (
    id           text PRIMARY KEY,
    workspace_id text NOT NULL REFERENCES workspaces(id),
    display_name text NOT NULL,
    kind         text NOT NULL,          -- iris | openweight | commercial
    base_url     text NOT NULL,
    auth_ref     text,                   -- KMS/vault key reference, never the secret
    manifest     jsonb,                  -- last fetched, schema-validated
    manifest_fetched_at timestamptz,
    healthy      boolean NOT NULL DEFAULT false,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE generation_jobs (
    id            text PRIMARY KEY,
    workspace_id  text NOT NULL REFERENCES workspaces(id),
    project_id    text NOT NULL REFERENCES projects(id),
    endpoint_id   text NOT NULL REFERENCES model_endpoints(id),
    parent_job_id text REFERENCES generation_jobs(id), -- fan-out sub-jobs point at their parent
    depends_on_job_id text REFERENCES generation_jobs(id), -- chain ordering
    task          text NOT NULL,
    profile       text NOT NULL DEFAULT 'draft',
    request       jsonb NOT NULL,        -- resolved inference-API request (minus signed URLs)
    target_entity_id text,               -- sht_ | view slot | canvas selection
    state         text NOT NULL DEFAULT 'queued',
    -- queue mechanics (SKIP LOCKED claims):
    claimed_by    text,
    claimed_at    timestamptz,
    attempts      int NOT NULL DEFAULT 0,
    not_before    timestamptz NOT NULL DEFAULT now(), -- backoff scheduling
    progress      double precision NOT NULL DEFAULT 0,
    error_code    text,
    error_message text,
    cost_estimate numeric(12,4),
    cost_actual   numeric(12,4),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
-- Claim index: claimable work per queue, in order.
CREATE INDEX generation_jobs_claim_idx
    ON generation_jobs (state, not_before, created_at)
    WHERE state IN ('queued');
CREATE INDEX generation_jobs_project_idx ON generation_jobs (project_id, created_at DESC);

CREATE TABLE usage_events (
    id           bigserial PRIMARY KEY,
    workspace_id text NOT NULL,
    job_id       text,
    kind         text NOT NULL,          -- generation | media | render
    unit         text NOT NULL,          -- gpu_second | usd
    quantity     numeric(14,6) NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- ── Doc runtime (op logs; M3) ──────────────────────────────────────────────────

CREATE TABLE docs (
    id          text PRIMARY KEY,
    project_id  text NOT NULL REFERENCES projects(id),
    kind        text NOT NULL,  -- story | canvas | timeline
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE doc_ops (
    doc_id   text NOT NULL REFERENCES docs(id),
    seq      bigint NOT NULL,             -- server-assigned, gapless per doc
    actor_id text NOT NULL,
    op       jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (doc_id, seq)
);

CREATE TABLE doc_snapshots (
    doc_id     text NOT NULL REFERENCES docs(id),
    seq        bigint NOT NULL,           -- snapshot as of this op
    sha256     text NOT NULL,             -- snapshot body in object storage
    label      text,                      -- named versions
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (doc_id, seq)
);

-- +goose Down
DROP TABLE IF EXISTS doc_snapshots, doc_ops, docs, usage_events, generation_jobs,
    model_endpoints, sequence_edges, takes, shots, characters, views, sets, scenes,
    asset_links, asset_versions, assets, projects, members, workspaces CASCADE;
