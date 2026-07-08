package queue

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Generation queue: same lease/reaper/ownership semantics as media_jobs, plus
// dependency gating (chain regeneration) and the spec's error taxonomy
// (error_code + error_message instead of a single error column).
//
// Only sub-jobs (parent_job_id IS NOT NULL) are claimable — the parent row is
// the fan-out request whose state aggregates its children (see store side).
// Long-running dispatch (a video generation can poll for minutes) extends its
// lease via HeartbeatGenerationJob instead of relying on a huge lease window.

const GenerationChannel = "generation_jobs"

type GenerationJob struct {
	ID          string
	WorkspaceID string
	ProjectID   string
	EndpointID  string
	ParentJobID string
	Task        string
	Profile     string
	Request     []byte // resolved request JSON (prompt, refs as asset ids, output, params, seed)
	Attempts    int
}

// ClaimGenerationJobs claims runnable sub-jobs: queued, due, and either
// dependency-free or dependent on a completed job.
func ClaimGenerationJobs(ctx context.Context, pool *pgxpool.Pool, worker string, limit int) ([]*GenerationJob, error) {
	rows, err := pool.Query(ctx, `
		WITH next AS (
			SELECT id FROM generation_jobs
			WHERE state = 'queued' AND not_before <= now()
			  AND parent_job_id IS NOT NULL
			  AND (depends_on_job_id IS NULL OR depends_on_job_id IN
			       (SELECT id FROM generation_jobs WHERE state = 'complete'))
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE generation_jobs j
		SET state = 'dispatched', claimed_by = $2, claimed_at = now(),
		    attempts = attempts + 1, updated_at = now()
		FROM next WHERE j.id = next.id
		RETURNING j.id, j.workspace_id, j.project_id, j.endpoint_id,
		          COALESCE(j.parent_job_id, ''), j.task, j.profile, j.request, j.attempts`,
		limit, worker)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []*GenerationJob
	for rows.Next() {
		j := &GenerationJob{}
		if err := rows.Scan(&j.ID, &j.WorkspaceID, &j.ProjectID, &j.EndpointID,
			&j.ParentJobID, &j.Task, &j.Profile, &j.Request, &j.Attempts); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// HeartbeatGenerationJob extends the lease and records progress while the
// worker polls the model endpoint. ErrNotOwner means the job was reaped
// (canceled or lease raced) — the worker must abandon it.
func HeartbeatGenerationJob(ctx context.Context, pool *pgxpool.Pool, jobID, worker, state string, progress float64) error {
	tag, err := pool.Exec(ctx, `
		UPDATE generation_jobs
		SET claimed_at = now(), progress = $3, state = $4, updated_at = now()
		WHERE id = $1 AND claimed_by = $2 AND state IN ('dispatched', 'running')`,
		jobID, worker, progress, state)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotOwner
	}
	return nil
}

// CompleteGenerationJob finalizes a sub-job inside tx (alongside the asset/
// lineage writes the artifacts produced) and recomputes the parent aggregate.
func CompleteGenerationJob(ctx context.Context, tx pgx.Tx, jobID, worker string, costActual float64) error {
	tag, err := tx.Exec(ctx, `
		UPDATE generation_jobs
		SET state = 'complete', progress = 1, cost_actual = $3, updated_at = now()
		WHERE id = $1 AND claimed_by = $2 AND state IN ('dispatched', 'running')`,
		jobID, worker, costActual)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotOwner
	}
	if err := rollupParent(ctx, tx, jobID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `SELECT pg_notify($1, $2)`, GenerationChannel, jobID)
	return err
}

// FailGenerationJob records a failure with the spec's error taxonomy.
// Retryable failures requeue with backoff until maxAttempts.
func FailGenerationJob(ctx context.Context, pool *pgxpool.Pool, job *GenerationJob, worker, code, message string, retryable bool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var tag pgconn.CommandTag
	if retryable && job.Attempts < maxAttempts {
		backoff := backoffFor(job.Attempts)
		tag, err = tx.Exec(ctx, `
			UPDATE generation_jobs SET state = 'queued',
			       not_before = now() + make_interval(secs => $2),
			       error_code = $3, error_message = $4, updated_at = now()
			WHERE id = $1 AND claimed_by = $5 AND state IN ('dispatched', 'running')`,
			job.ID, backoff.Seconds(), code, message, worker)
	} else {
		tag, err = tx.Exec(ctx, `
			UPDATE generation_jobs SET state = 'failed',
			       error_code = $2, error_message = $3, updated_at = now()
			WHERE id = $1 AND claimed_by = $4 AND state IN ('dispatched', 'running')`,
			job.ID, code, message, worker)
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotOwner
	}
	if err := rollupParent(ctx, tx, job.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`, GenerationChannel, job.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// rollupParent recomputes the parent job's aggregate state and progress from
// its children: complete when all terminal and ≥1 complete; failed when all
// terminal and none complete; else running with mean progress.
func rollupParent(ctx context.Context, tx pgx.Tx, subJobID string) error {
	_, err := tx.Exec(ctx, `
		WITH parent AS (
			SELECT parent_job_id AS id FROM generation_jobs WHERE id = $1 AND parent_job_id IS NOT NULL
		), agg AS (
			SELECT count(*) AS total,
			       count(*) FILTER (WHERE state IN ('complete','failed','canceled')) AS terminal,
			       count(*) FILTER (WHERE state = 'complete') AS ok,
			       avg(progress) AS progress,
			       sum(cost_actual) AS cost_actual
			FROM generation_jobs WHERE parent_job_id = (SELECT id FROM parent)
		)
		UPDATE generation_jobs p
		SET state = CASE
		        WHEN agg.terminal = agg.total AND agg.ok > 0 THEN 'complete'
		        WHEN agg.terminal = agg.total THEN 'failed'
		        ELSE 'running'
		    END,
		    progress = COALESCE(agg.progress, 0),
		    cost_actual = agg.cost_actual,
		    updated_at = now()
		FROM agg
		WHERE p.id = (SELECT id FROM parent)`, subJobID)
	return err
}

// ReapStaleGenerationJobs mirrors the media reaper for the generation table.
func ReapStaleGenerationJobs(ctx context.Context, pool *pgxpool.Pool) (requeued, parked int64, err error) {
	tag, err := pool.Exec(ctx, `
		UPDATE generation_jobs
		SET state = 'queued', not_before = now(), updated_at = now(),
		    error_code = 'transient', error_message = 'reaped: lease expired'
		WHERE state IN ('dispatched','running') AND parent_job_id IS NOT NULL
		  AND claimed_at < now() - make_interval(secs => $1)
		  AND attempts < $2`,
		Lease.Seconds(), maxAttempts)
	if err != nil {
		return 0, 0, err
	}
	requeued = tag.RowsAffected()
	tag, err = pool.Exec(ctx, `
		UPDATE generation_jobs
		SET state = 'failed', updated_at = now(),
		    error_code = 'transient', error_message = 'reaped: lease expired, attempts exhausted'
		WHERE state IN ('dispatched','running') AND parent_job_id IS NOT NULL
		  AND claimed_at < now() - make_interval(secs => $1)`,
		Lease.Seconds())
	if err != nil {
		return requeued, 0, err
	}
	// Terminal children may have arrived while their parent was mid-flight;
	// reconcile any parent stuck non-terminal whose children are all done.
	if _, err := pool.Exec(ctx, `
		UPDATE generation_jobs p
		SET state = CASE WHEN ok > 0 THEN 'complete' ELSE 'failed' END, updated_at = now()
		FROM (
			SELECT parent_job_id AS id,
			       count(*) FILTER (WHERE state = 'complete') AS ok
			FROM generation_jobs
			WHERE parent_job_id IS NOT NULL
			GROUP BY parent_job_id
			HAVING count(*) = count(*) FILTER (WHERE state IN ('complete','failed','canceled'))
		) done
		WHERE p.id = done.id AND p.state NOT IN ('complete','failed','canceled')`); err != nil {
		return requeued, tag.RowsAffected(), err
	}
	return requeued, tag.RowsAffected(), nil
}

func backoffFor(attempts int) time.Duration {
	d := 5 * time.Second
	for i := 1; i < attempts; i++ {
		d *= 2
	}
	return d
}
