-- +goose Up
-- Generation response cache (dev/test helper): identical semantic requests
-- reuse the landed artifact instead of re-billing the endpoint. Keyed by a
-- hash of the RESOLVED request (model identity, prompt, seed, output,
-- params, reference/conditioning VERSIONS — never presigned URLs, which
-- change per dispatch). Enabled per-orchestrator via IRIS_GEN_CACHE; only
-- successful landings are recorded.
CREATE TABLE generation_cache (
    request_hash text PRIMARY KEY,
    sha256       text NOT NULL,
    size_bytes   bigint NOT NULL,
    content_type text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
