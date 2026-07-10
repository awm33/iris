package store

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/awm33/iris/backend/internal/ids"
	"github.com/awm33/iris/backend/internal/queue"
)

// GenJob mirrors a generation_jobs row for API reads.
type GenJob struct {
	ID, WorkspaceID, ProjectID, EndpointID string
	ParentJobID, DependsOnJobID            string
	Task, Profile, State                   string
	Request                                json.RawMessage
	TargetEntityID                         string
	Progress                               float64
	ErrorCode, ErrorMessage                string
	CostEstimate, CostActual               float64
	ArtifactVersionIDs                     []string
	CreatedAt, UpdatedAt                   Time
}

// GenRequest is the resolved request stored on each job row (asset ids, not
// URLs — URLs are signed at dispatch time so they're never stale).
type GenRequest struct {
	Prompt         string           `json:"prompt"`
	NegativePrompt string           `json:"negative_prompt,omitempty"`
	Seed           int64            `json:"seed,omitempty"`
	Count          int              `json:"count,omitempty"` // parent only
	Output         json.RawMessage  `json:"output,omitempty"`
	References     []GenRef         `json:"references,omitempty"`
	Conditioning   *GenConditioning `json:"conditioning,omitempty"`
	// Resolve first_frame at dispatch from the dependency's landed artifact.
	CarryFromDependsOn bool            `json:"carry_from_depends_on,omitempty"`
	Params             json.RawMessage `json:"params,omitempty"`
}

type GenRef struct {
	Kind      string  `json:"kind"`
	Role      string  `json:"role"`
	AssetID   string  `json:"asset_id"`
	VersionID string  `json:"version_id,omitempty"` // "" = head at dispatch
	Weight    float64 `json:"weight,omitempty"`
}

// GenConditioning stores conditioning inputs as asset refs (ids, not URLs —
// URLs are signed at dispatch). Gen-fill (M4) uses source_image + mask; the
// remaining spec conditioning keys wire up with the surfaces that need them.
type GenConditioning struct {
	// FirstFrame carries continuity: an image version is used as-is; a VIDEO
	// version means "its last frame" (the prep artifact) — the W3 carry.
	FirstFrame  *GenRef `json:"first_frame,omitempty"`
	SourceImage *GenRef `json:"source_image,omitempty"`
	// SourceVideo: the clip a lipsync_post pass re-times to the audio ref.
	SourceVideo *GenRef `json:"source_video,omitempty"`
	Mask        *GenRef `json:"mask,omitempty"`
}

// CreateGenerationFanout inserts the parent job + count sub-jobs in one
// transaction and NOTIFYs on commit. Sub-jobs get distinct seeds when the
// request pins one (seed, seed+1, …) so takes differ deterministically.
// resolveRandomSeeds turns seed-0 ("random") into concrete per-sub-job seeds
// — pass the endpoint's manifest.features.seed: inventing a seed for an
// endpoint that rejects seeds would fail every random job at dispatch.
func (s *Store) CreateGenerationFanout(ctx context.Context, parent *GenJob, count int, resolveRandomSeeds bool) (subIDs []string, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	parent.ID = ids.New("job")
	if _, err := tx.Exec(ctx, `
		INSERT INTO generation_jobs
			(id, workspace_id, project_id, endpoint_id, task, profile, request,
			 target_entity_id, state, cost_estimate, depends_on_job_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''),'running',$9,NULLIF($10,''))`,
		parent.ID, parent.WorkspaceID, parent.ProjectID, parent.EndpointID,
		parent.Task, parent.Profile, parent.Request, parent.TargetEntityID,
		parent.CostEstimate, parent.DependsOnJobID); err != nil {
		return nil, err
	}

	var req GenRequest
	_ = json.Unmarshal(parent.Request, &req)
	for i := 0; i < count; i++ {
		sub := req
		sub.Count = 0
		if req.Seed != 0 {
			sub.Seed = req.Seed + int64(i)
		} else if resolveRandomSeeds {
			// "Random" resolves to a CONCRETE seed here, not at the endpoint:
			// every sub-job differs, the recipe records the real seed, and
			// regenerate-from-this reproduces the exact take (spec: endpoints
			// honor seeds deterministically). Seedless endpoints keep 0 — the
			// recipe honestly records "random".
			sub.Seed = randSeed()
		}
		subJSON, _ := json.Marshal(sub)
		id := ids.New("job")
		subIDs = append(subIDs, id)
		if _, err := tx.Exec(ctx, `
			INSERT INTO generation_jobs
				(id, workspace_id, project_id, endpoint_id, parent_job_id, task,
				 profile, request, target_entity_id, state, depends_on_job_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,''),'queued',NULLIF($10,''))`,
			id, parent.WorkspaceID, parent.ProjectID, parent.EndpointID, parent.ID,
			parent.Task, parent.Profile, subJSON, parent.TargetEntityID,
			parent.DependsOnJobID); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `SELECT pg_notify($1, '')`, queue.GenerationChannel); err != nil {
		return nil, err
	}
	return subIDs, tx.Commit(ctx)
}

// randSeed: non-zero (zero means "random" in the request vocabulary) and
// within JS safe-integer range — seeds round-trip through the web UI's
// recipe JSON, and 2^53+ would corrupt there.
func randSeed() int64 {
	return rand.Int64N(1<<53-2) + 1
}

const genJobCols = `id, workspace_id, project_id, endpoint_id,
	COALESCE(parent_job_id,''), COALESCE(depends_on_job_id,''), task, profile,
	state, request, COALESCE(target_entity_id,''), progress,
	COALESCE(error_code,''), COALESCE(error_message,''),
	COALESCE(cost_estimate,0), COALESCE(cost_actual,0), created_at, updated_at`

func scanGenJob(row interface{ Scan(...any) error }) (*GenJob, error) {
	j := &GenJob{}
	err := row.Scan(&j.ID, &j.WorkspaceID, &j.ProjectID, &j.EndpointID,
		&j.ParentJobID, &j.DependsOnJobID, &j.Task, &j.Profile,
		&j.State, &j.Request, &j.TargetEntityID, &j.Progress,
		&j.ErrorCode, &j.ErrorMessage,
		&j.CostEstimate, &j.CostActual, &j.CreatedAt, &j.UpdatedAt)
	return j, err
}

func (s *Store) GetGenJob(ctx context.Context, id string) (*GenJob, error) {
	j, err := scanGenJob(s.pool.QueryRow(ctx,
		`SELECT `+genJobCols+` FROM generation_jobs WHERE id = $1`, id))
	if err != nil {
		return nil, wrapNotFound(err)
	}
	// Artifacts land as lineage edges; surface their version ids on the job.
	// Ordered like ListGenJobs — candidate order must be stable across calls
	// (index-based UIs) and consistent between list and detail.
	rows, err := s.pool.Query(ctx, `
		SELECT from_version_id FROM (
			SELECT from_version_id, created_at FROM asset_links
			WHERE to_entity_id = $1 AND role = 'generated_by'
			UNION
			SELECT l.from_version_id, l.created_at FROM asset_links l
			JOIN generation_jobs sub ON sub.id = l.to_entity_id
			WHERE sub.parent_job_id = $1 AND l.role = 'generated_by'
		) u ORDER BY created_at, from_version_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		j.ArtifactVersionIDs = append(j.ArtifactVersionIDs, v)
	}
	return j, rows.Err()
}

// ListGenJobs returns parent jobs (the user-facing units) newest-first, with
// artifact version ids aggregated from their sub-jobs' lineage edges — the
// Jobs UI renders thumbnails straight from the list.
func (s *Store) ListGenJobs(ctx context.Context, projectID, state string) ([]*GenJob, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+genJobCols+`,
		       COALESCE(art.ids, '{}') AS artifact_ids
		FROM generation_jobs g
		LEFT JOIN LATERAL (
			SELECT array_agg(l.from_version_id ORDER BY l.created_at, l.from_version_id) AS ids
			FROM asset_links l
			JOIN generation_jobs sub ON sub.id = l.to_entity_id
			WHERE sub.parent_job_id = g.id AND l.role = 'generated_by'
		) art ON true
		WHERE g.project_id = $1 AND g.parent_job_id IS NULL
		  AND ($2 = '' OR g.state = $2)
		ORDER BY g.created_at DESC LIMIT 100`, projectID, state)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*GenJob
	for rows.Next() {
		j := &GenJob{}
		if err := rows.Scan(&j.ID, &j.WorkspaceID, &j.ProjectID, &j.EndpointID,
			&j.ParentJobID, &j.DependsOnJobID, &j.Task, &j.Profile,
			&j.State, &j.Request, &j.TargetEntityID, &j.Progress,
			&j.ErrorCode, &j.ErrorMessage,
			&j.CostEstimate, &j.CostActual, &j.CreatedAt, &j.UpdatedAt,
			&j.ArtifactVersionIDs); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (s *Store) ListSubJobs(ctx context.Context, parentID string) ([]*GenJob, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+genJobCols+` FROM generation_jobs
		WHERE parent_job_id = $1 ORDER BY created_at`, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*GenJob
	for rows.Next() {
		j, err := scanGenJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// CancelGeneration marks queued/in-flight rows canceled (parent + children).
// Endpoint-side cancellation is the owning worker's job: it detects the state
// flip on its next heartbeat and cancels with the per-attempt id only it
// knows. Canceling an already-terminal job is an idempotent no-op.
func (s *Store) CancelGeneration(ctx context.Context, jobID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock children before the parent — the completion path locks its own
	// child row then the parent (rollup), so matching that order avoids
	// deadlock aborts between cancel and a completing worker.
	tag, err := tx.Exec(ctx, `
		WITH victims AS (
			SELECT id FROM generation_jobs
			WHERE (id = $1 OR parent_job_id = $1)
			  AND state IN ('queued', 'dispatched', 'running')
			ORDER BY (parent_job_id IS NULL), id
			FOR UPDATE
		)
		UPDATE generation_jobs j
		SET state = 'canceled', updated_at = now()
		FROM victims WHERE j.id = victims.id`, jobID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM generation_jobs WHERE id = $1)`, jobID).
			Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		return nil // already terminal — idempotent
	}
	// If the target was a sub-job, its parent aggregate must reflect the
	// cancellation; if it was a parent, dependents gate on it and must fail.
	if err := queue.RollupAfterCancel(ctx, tx, jobID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`, queue.GenerationChannel, jobID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RecordUsage writes a usage event (cost metering).
func (s *Store) RecordUsage(ctx context.Context, workspaceID, jobID, kind, unit string, qty float64) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO usage_events (workspace_id, job_id, kind, unit, quantity)
		VALUES ($1, $2, $3, $4, $5)`, workspaceID, jobID, kind, unit, qty)
	return err
}

// Pool exposes the pgx pool for packages that need transaction control with
// queue helpers (the orchestrator's artifact-landing transaction).
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// GenCacheEntry: a previously landed artifact for an identical resolved
// request — the dev/test cache that keeps repeated runs off the bill.
type GenCacheEntry struct {
	SHA256      string
	SizeBytes   int64
	ContentType string
}

func (s *Store) GetGenCache(ctx context.Context, requestHash string) (*GenCacheEntry, error) {
	e := &GenCacheEntry{}
	err := s.pool.QueryRow(ctx,
		`SELECT sha256, size_bytes, content_type FROM generation_cache WHERE request_hash = $1`,
		requestHash).Scan(&e.SHA256, &e.SizeBytes, &e.ContentType)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return e, err
}

func (s *Store) PutGenCache(ctx context.Context, requestHash, sha256, contentType string, sizeBytes int64) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO generation_cache (request_hash, sha256, size_bytes, content_type)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (request_hash) DO UPDATE
		SET sha256 = EXCLUDED.sha256, size_bytes = EXCLUDED.size_bytes,
		    content_type = EXCLUDED.content_type, created_at = now()`,
		requestHash, sha256, sizeBytes, contentType)
	return err
}
