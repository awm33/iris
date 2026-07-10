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
	ID             string
	WorkspaceID    string
	ProjectID      string
	EndpointID     string
	ParentJobID    string
	TargetEntityID string // where results land (sht_… = shot takes); "" = library only
	Task           string
	Profile        string
	Request        []byte // resolved request JSON (prompt, refs as asset ids, output, params, seed)
	DependsOnJobID string // set = the dispatch-time carry source (chain regeneration)
	Attempts       int
	RemoteHandle   []byte // adapter's persisted remote pointer (re-attach on reclaim)
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
		          COALESCE(j.parent_job_id, ''), COALESCE(j.target_entity_id, ''),
		          j.task, j.profile, j.request, COALESCE(j.depends_on_job_id, ''), j.attempts,
		          j.remote_handle`,
		limit, worker)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []*GenerationJob
	for rows.Next() {
		j := &GenerationJob{}
		if err := rows.Scan(&j.ID, &j.WorkspaceID, &j.ProjectID, &j.EndpointID,
			&j.ParentJobID, &j.TargetEntityID, &j.Task, &j.Profile, &j.Request,
			&j.DependsOnJobID, &j.Attempts, &j.RemoteHandle); err != nil {
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
		SET state = 'complete', progress = 1, cost_actual = $3,
		    error_code = NULL, error_message = NULL, updated_at = now()
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

// RequeueGenerationJobUnreachable requeues a sub-job whose endpoint was
// unreachable WITHOUT burning an attempt — endpoint restarts are expected
// (spec §4 overloaded/transient) and shouldn't park jobs in ~20s of backoff.
// Bounded by job age: after maxUnreachableAge the job parks instead.
const maxUnreachableAge = time.Hour

func RequeueGenerationJobUnreachable(ctx context.Context, pool *pgxpool.Pool, job *GenerationJob, worker, message string) error {
	tag, err := pool.Exec(ctx, `
		UPDATE generation_jobs
		SET state = CASE WHEN created_at < now() - make_interval(secs => $4) THEN 'failed' ELSE 'queued' END,
		    attempts = CASE WHEN created_at < now() - make_interval(secs => $4) THEN attempts ELSE attempts - 1 END,
		    not_before = now() + make_interval(secs => 15),
		    error_code = 'overloaded', error_message = $3, updated_at = now()
		WHERE id = $1 AND claimed_by = $2 AND state IN ('dispatched', 'running')`,
		job.ID, worker, message, maxUnreachableAge.Seconds())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotOwner
	}
	return nil
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
//
// The parent row is locked BEFORE the aggregate is computed: under READ
// COMMITTED, a blocked UPDATE re-evaluates only its qual after the lock
// clears — a pre-computed aggregate CTE would be stale, and two children
// finishing concurrently could leave the parent stuck 'running' forever.
// Lock-then-aggregate serializes sibling rollups correctly.
func rollupParent(ctx context.Context, tx pgx.Tx, subJobID string) error {
	var parentID *string
	if err := tx.QueryRow(ctx, `
		SELECT parent_job_id FROM generation_jobs WHERE id = $1`, subJobID).
		Scan(&parentID); err != nil {
		return err
	}
	if parentID == nil {
		return nil
	}
	return rollupParentByID(ctx, tx, *parentID)
}

func rollupParentByID(ctx context.Context, tx pgx.Tx, parentID string) error {
	if _, err := tx.Exec(ctx, `
		SELECT 1 FROM generation_jobs WHERE id = $1 FOR UPDATE`, parentID); err != nil {
		return err
	}
	var prevState, newState string
	err := tx.QueryRow(ctx, `
		WITH agg AS (
			SELECT count(*) AS total,
			       count(*) FILTER (WHERE state IN ('complete','failed','canceled')) AS terminal,
			       count(*) FILTER (WHERE state = 'complete') AS ok,
			       avg(progress) AS progress,
			       sum(cost_actual) AS cost_actual
			FROM generation_jobs WHERE parent_job_id = $1
		), prev AS (
			SELECT state FROM generation_jobs WHERE id = $1
		)
		UPDATE generation_jobs p
		SET state = CASE
		        WHEN p.state = 'canceled' THEN 'canceled'
		        WHEN agg.terminal = agg.total AND agg.ok > 0 THEN 'complete'
		        WHEN agg.terminal = agg.total THEN 'failed'
		        ELSE 'running'
		    END,
		    progress = COALESCE(agg.progress, 0),
		    cost_actual = agg.cost_actual,
		    updated_at = now()
		FROM agg, prev
		WHERE p.id = $1
		RETURNING prev.state, p.state`, parentID).Scan(&prevState, &newState)
	if err != nil {
		return err
	}
	// Dependency-failure propagation (C1): dependents gate on this parent
	// reaching 'complete'; if it terminalized any other way they can never
	// run — fail them now (transitively), same transaction.
	if newState != prevState && (newState == "failed" || newState == "canceled") {
		return FailDependents(ctx, tx, parentID)
	}
	return nil
}

// RollupAfterCancel reconciles aggregates after CancelGeneration: if the
// canceled id was a sub-job its parent is re-rolled; if it was a parent, its
// dependents can never run and are failed transitively.
func RollupAfterCancel(ctx context.Context, tx pgx.Tx, jobID string) error {
	var parentID *string
	if err := tx.QueryRow(ctx,
		`SELECT parent_job_id FROM generation_jobs WHERE id = $1`, jobID).
		Scan(&parentID); err != nil {
		return err
	}
	if parentID != nil {
		return rollupParentDependentsOnly(ctx, tx, *parentID)
	}
	return FailDependents(ctx, tx, jobID)
}

// FailDependents fails all queued sub-jobs (and re-rolls their parents,
// transitively) whose depends_on_job_id will never complete.
func FailDependents(ctx context.Context, tx pgx.Tx, failedJobID string) error {
	frontier := []string{failedJobID}
	for len(frontier) > 0 {
		next := []string{}
		for _, dep := range frontier {
			rows, err := tx.Query(ctx, `
				UPDATE generation_jobs
				SET state = 'failed', error_code = 'dependency_failed',
				    error_message = 'dependency ' || $1 || ' did not complete',
				    updated_at = now()
				WHERE state = 'queued' AND depends_on_job_id = $1
				  AND parent_job_id IS NOT NULL
				RETURNING parent_job_id`, dep)
			if err != nil {
				return err
			}
			parents := map[string]bool{}
			for rows.Next() {
				var p string
				if err := rows.Scan(&p); err != nil {
					rows.Close()
					return err
				}
				parents[p] = true
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			for p := range parents {
				if err := rollupParentDependentsOnly(ctx, tx, p); err != nil {
					return err
				}
				next = append(next, p)
			}
		}
		frontier = next
	}
	return nil
}

// rollupParentDependentsOnly is rollupParentByID without re-entering
// FailDependents (the caller's loop handles transitivity).
func rollupParentDependentsOnly(ctx context.Context, tx pgx.Tx, parentID string) error {
	if _, err := tx.Exec(ctx, `
		SELECT 1 FROM generation_jobs WHERE id = $1 FOR UPDATE`, parentID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		WITH agg AS (
			SELECT count(*) AS total,
			       count(*) FILTER (WHERE state IN ('complete','failed','canceled')) AS terminal,
			       count(*) FILTER (WHERE state = 'complete') AS ok,
			       avg(progress) AS progress,
			       sum(cost_actual) AS cost_actual
			FROM generation_jobs WHERE parent_job_id = $1
		)
		UPDATE generation_jobs p
		SET state = CASE
		        WHEN p.state = 'canceled' THEN 'canceled'
		        WHEN agg.terminal = agg.total AND agg.ok > 0 THEN 'complete'
		        WHEN agg.terminal = agg.total THEN 'failed'
		        ELSE 'running'
		    END,
		    progress = COALESCE(agg.progress, 0),
		    cost_actual = agg.cost_actual,
		    updated_at = now()
		FROM agg WHERE p.id = $1`, parentID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `SELECT pg_notify($1, $2)`, GenerationChannel, parentID)
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
	parked = tag.RowsAffected()

	// Backstop for dependency-failure propagation (races, retries created
	// against an already-failed dependency): park queued sub-jobs whose
	// dependency is terminally non-complete, then reconcile their parents.
	if err := reapTx(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			UPDATE generation_jobs
			SET state = 'failed', error_code = 'dependency_failed',
			    error_message = 'dependency ' || depends_on_job_id || ' did not complete',
			    updated_at = now()
			WHERE state = 'queued' AND parent_job_id IS NOT NULL
			  AND depends_on_job_id IN
			      (SELECT id FROM generation_jobs WHERE state IN ('failed','canceled'))
			RETURNING parent_job_id`)
		if err != nil {
			return err
		}
		parents := map[string]bool{}
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return err
			}
			parents[p] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for p := range parents {
			if err := rollupParentDependentsOnly(ctx, tx, p); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return requeued, parked, err
	}

	// Terminal children may have arrived while their parent rollup raced
	// (see rollupParentByID); reconcile stuck parents with a FULL recompute
	// (state + progress + cost) and notify — a repaired state without its
	// progress/cost, or without a NOTIFY, is still wrong.
	if err := reapTx(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT parent_job_id
			FROM generation_jobs
			WHERE parent_job_id IS NOT NULL
			GROUP BY parent_job_id
			HAVING count(*) = count(*) FILTER (WHERE state IN ('complete','failed','canceled'))
			   AND parent_job_id IN
			       (SELECT id FROM generation_jobs
			        WHERE state NOT IN ('complete','failed','canceled'))`)
		if err != nil {
			return err
		}
		var parents []string
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return err
			}
			parents = append(parents, p)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, p := range parents {
			if err := rollupParentByID(ctx, tx, p); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`, GenerationChannel, p); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return requeued, parked, err
	}
	return requeued, parked, nil
}

func reapTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func backoffFor(attempts int) time.Duration {
	d := 5 * time.Second
	for i := 1; i < attempts; i++ {
		d *= 2
	}
	return d
}

// SetGenerationJobHandle persists the adapter's remote pointer, ownership-
// guarded: a reaped job's stale goroutine must not overwrite the handle a
// newer attempt is using.
func SetGenerationJobHandle(ctx context.Context, pool *pgxpool.Pool, jobID, worker string, handle []byte) error {
	tag, err := pool.Exec(ctx, `
		UPDATE generation_jobs SET remote_handle = $3, updated_at = now()
		WHERE id = $1 AND claimed_by = $2 AND state IN ('dispatched', 'running')`,
		jobID, worker, handle)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotOwner
	}
	return nil
}
