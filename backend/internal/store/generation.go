package store

import (
	"context"
	"encoding/json"

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
	Prompt         string          `json:"prompt"`
	NegativePrompt string          `json:"negative_prompt,omitempty"`
	Seed           int64           `json:"seed,omitempty"`
	Count          int             `json:"count,omitempty"` // parent only
	Output         json.RawMessage `json:"output,omitempty"`
	References     []GenRef        `json:"references,omitempty"`
	Params         json.RawMessage `json:"params,omitempty"`
}

type GenRef struct {
	Kind      string  `json:"kind"`
	Role      string  `json:"role"`
	AssetID   string  `json:"asset_id"`
	VersionID string  `json:"version_id,omitempty"` // "" = head at dispatch
	Weight    float64 `json:"weight,omitempty"`
}

// CreateGenerationFanout inserts the parent job + count sub-jobs in one
// transaction and NOTIFYs on commit. Sub-jobs get distinct seeds when the
// request pins one (seed, seed+1, …) so takes differ deterministically.
func (s *Store) CreateGenerationFanout(ctx context.Context, parent *GenJob, count int) (subIDs []string, err error) {
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
	rows, err := s.pool.Query(ctx, `
		SELECT from_version_id FROM asset_links
		WHERE to_entity_id = $1 AND role = 'generated_by'
		UNION
		SELECT l.from_version_id FROM asset_links l
		JOIN generation_jobs sub ON sub.id = l.to_entity_id
		WHERE sub.parent_job_id = $1 AND l.role = 'generated_by'`, id)
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

// ListGenJobs returns parent jobs (the user-facing units) newest-first.
func (s *Store) ListGenJobs(ctx context.Context, projectID, state string) ([]*GenJob, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+genJobCols+` FROM generation_jobs
		WHERE project_id = $1 AND parent_job_id IS NULL
		  AND ($2 = '' OR state = $2)
		ORDER BY created_at DESC LIMIT 100`, projectID, state)
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

// CancelGeneration marks queued/in-flight rows canceled (parent + children)
// and returns the endpoint-side ids of dispatched sub-jobs so the caller can
// cancel them remotely. Workers detect the state change on heartbeat.
func (s *Store) CancelGeneration(ctx context.Context, jobID string) (dispatched []string, err error) {
	// RETURNING sees post-update values, so the pre-update state must be
	// captured in the CTE to know which sub-jobs were already dispatched.
	rows, err := s.pool.Query(ctx, `
		WITH victims AS (
			SELECT id, state AS old_state FROM generation_jobs
			WHERE (id = $1 OR parent_job_id = $1)
			  AND state IN ('queued', 'dispatched', 'running')
			FOR UPDATE
		)
		UPDATE generation_jobs j
		SET state = 'canceled', updated_at = now()
		FROM victims WHERE j.id = victims.id
		RETURNING j.id, victims.old_state IN ('dispatched','running')`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var id string
		var wasDispatched bool
		if err := rows.Scan(&id, &wasDispatched); err != nil {
			return nil, err
		}
		found = true
		if wasDispatched && id != jobID {
			dispatched = append(dispatched, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrNotFound
	}
	return dispatched, nil
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
