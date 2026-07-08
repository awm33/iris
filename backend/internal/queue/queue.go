// Package queue implements the Postgres-native job queue:
// FOR UPDATE SKIP LOCKED claims + LISTEN/NOTIFY wakeups (TDD §3.1).
//
// Delivery guarantee: at-least-once. A claim leases the job (claimed_by/
// claimed_at); jobs stranded in 'running' past the lease (worker crash, OOM,
// SIGTERM mid-job) are reaped — requeued while attempts remain, parked as
// failed once exhausted. Completion and failure updates are guarded by
// (state='running' AND claimed_by=worker) so a reaped-and-reclaimed job can't
// be double-completed by its original owner; handlers must therefore be
// idempotent (the probe handler is).
//
// media_jobs is the first consumer (M1). generation_jobs (M2) will reuse the
// same semantics with its added depends_on gating:
//
//	... AND (depends_on_job_id IS NULL OR depends_on_job_id IN
//	         (SELECT id FROM generation_jobs WHERE state = 'complete'))
//
// Completion runs in the SAME transaction as the domain writes it produces,
// followed by pg_notify — NOTIFY fires on commit, so dependency release and
// event fan-out are atomic with the results they announce.
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/awm33/iris/backend/internal/ids"
)

const (
	MediaChannel = "media_jobs"
	maxAttempts  = 3

	// Lease must exceed the worker's per-job timeout with margin; a healthy
	// worker always resolves a job well inside it.
	Lease = 5 * time.Minute
)

// ErrNotOwner is returned when a completion/failure update matched no row —
// the job was reaped (lease expired) and possibly reclaimed by another worker.
var ErrNotOwner = errors.New("job no longer owned by this worker")

type MediaJob struct {
	ID          string
	WorkspaceID string
	Kind        string // probe | proxy | filmstrip | waveform
	Input       json.RawMessage
	Attempts    int
}

// EnqueueMediaJob inserts a job and schedules a NOTIFY inside the caller's
// transaction — atomic with whatever domain write produced the job.
func EnqueueMediaJob(ctx context.Context, tx pgx.Tx, workspaceID, kind string, input any) error {
	in, err := json.Marshal(input)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO media_jobs (id, workspace_id, kind, input)
		VALUES ($1, $2, $3, $4)`,
		ids.New("mjob"), workspaceID, kind, in); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `SELECT pg_notify($1, '')`, MediaChannel)
	return err
}

// ClaimMediaJobs atomically claims up to limit runnable jobs for worker.
func ClaimMediaJobs(ctx context.Context, pool *pgxpool.Pool, worker string, limit int) ([]*MediaJob, error) {
	rows, err := pool.Query(ctx, `
		WITH next AS (
			SELECT id FROM media_jobs
			WHERE state = 'queued' AND not_before <= now()
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE media_jobs j
		SET state = 'running', claimed_by = $2, claimed_at = now(),
		    attempts = attempts + 1, updated_at = now()
		FROM next WHERE j.id = next.id
		RETURNING j.id, j.workspace_id, j.kind, j.input, j.attempts`,
		limit, worker)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []*MediaJob
	for rows.Next() {
		j := &MediaJob{}
		if err := rows.Scan(&j.ID, &j.WorkspaceID, &j.Kind, &j.Input, &j.Attempts); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// ReapStaleMediaJobs recovers jobs stranded in 'running' past the lease:
// requeued while attempts remain, parked as failed once exhausted. Run
// periodically by every worker; the guarded complete/fail updates make the
// race with a still-alive slow owner safe.
func ReapStaleMediaJobs(ctx context.Context, pool *pgxpool.Pool) (requeued, parked int64, err error) {
	tag, err := pool.Exec(ctx, `
		UPDATE media_jobs
		SET state = 'queued', not_before = now(), updated_at = now(),
		    error = COALESCE(error, '') || ' [reaped: lease expired]'
		WHERE state = 'running' AND claimed_at < now() - make_interval(secs => $1)
		  AND attempts < $2`,
		Lease.Seconds(), maxAttempts)
	if err != nil {
		return 0, 0, err
	}
	requeued = tag.RowsAffected()
	tag, err = pool.Exec(ctx, `
		UPDATE media_jobs
		SET state = 'failed', updated_at = now(),
		    error = COALESCE(error, '') || ' [reaped: lease expired, attempts exhausted]'
		WHERE state = 'running' AND claimed_at < now() - make_interval(secs => $1)`,
		Lease.Seconds())
	if err != nil {
		return requeued, 0, err
	}
	return requeued, tag.RowsAffected(), nil
}

// CompleteMediaJob marks the job done inside tx (commit alongside the domain
// writes the job produced) and schedules the completion NOTIFY. Returns
// ErrNotOwner — and the caller must roll back — if the lease was lost.
func CompleteMediaJob(ctx context.Context, tx pgx.Tx, jobID, worker string) error {
	tag, err := tx.Exec(ctx, `
		UPDATE media_jobs SET state = 'complete', error = NULL, updated_at = now()
		WHERE id = $1 AND state = 'running' AND claimed_by = $2`, jobID, worker)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotOwner
	}
	_, err = tx.Exec(ctx, `SELECT pg_notify($1, $2)`, MediaChannel, jobID)
	return err
}

// FailMediaJob records a failure: retryable failures requeue with exponential
// backoff until maxAttempts, then the job parks as failed. The update is
// ownership-guarded (worker must still hold the lease); losing the lease is
// reported as ErrNotOwner.
//
// Callers should pass a context that survives shutdown (not the canceled
// worker context) so failures during SIGTERM still get recorded.
func FailMediaJob(ctx context.Context, pool *pgxpool.Pool, job *MediaJob, worker string, jobErr error, retryable bool) error {
	var (
		tag pgconn.CommandTag
		err error
	)
	if retryable && job.Attempts < maxAttempts {
		backoff := time.Duration(math.Pow(2, float64(job.Attempts))) * 5 * time.Second
		tag, err = pool.Exec(ctx, `
			UPDATE media_jobs SET state = 'queued',
			       not_before = now() + make_interval(secs => $2),
			       error = $3, updated_at = now()
			WHERE id = $1 AND state = 'running' AND claimed_by = $4`,
			job.ID, backoff.Seconds(), jobErr.Error(), worker)
	} else {
		tag, err = pool.Exec(ctx, `
			UPDATE media_jobs SET state = 'failed', error = $2, updated_at = now()
			WHERE id = $1 AND state = 'running' AND claimed_by = $3`,
			job.ID, jobErr.Error(), worker)
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotOwner
	}
	return nil
}

// Listener wakes on NOTIFY with a poll-interval fallback, so a missed
// notification only delays work — never loses it.
type Listener struct {
	conn    *pgx.Conn
	channel string
	poll    time.Duration
}

func NewListener(ctx context.Context, dsn, channel string, poll time.Duration) (*Listener, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{channel}.Sanitize()); err != nil {
		conn.Close(ctx)
		return nil, err
	}
	return &Listener{conn: conn, channel: channel, poll: poll}, nil
}

// Wait blocks until a notification arrives, the poll interval elapses, or ctx
// is done. It returns nil in the first two cases (caller re-claims either
// way); a non-nil error means the connection is unusable and the Listener
// should be recreated.
func (l *Listener) Wait(ctx context.Context) error {
	waitCtx, cancel := context.WithTimeout(ctx, l.poll)
	defer cancel()
	_, err := l.conn.WaitForNotification(waitCtx)
	if err != nil && waitCtx.Err() != nil && ctx.Err() == nil {
		return nil // poll fallback tick
	}
	return err
}

func (l *Listener) Close(ctx context.Context) { _ = l.conn.Close(ctx) }
