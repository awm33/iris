-- +goose Up
-- Parent rollups, reaper reconciliation, and artifact lookups all key on
-- parent_job_id; without an index each is a seq scan growing with history.
CREATE INDEX generation_jobs_parent_idx
    ON generation_jobs (parent_job_id)
    WHERE parent_job_id IS NOT NULL;

-- Dependency-failure propagation scans by depends_on_job_id.
CREATE INDEX generation_jobs_depends_idx
    ON generation_jobs (depends_on_job_id)
    WHERE depends_on_job_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS generation_jobs_parent_idx;
DROP INDEX IF EXISTS generation_jobs_depends_idx;
