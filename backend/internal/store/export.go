package store

import (
	"context"

	"github.com/awm33/iris/backend/internal/ids"
	"github.com/awm33/iris/backend/internal/queue"
)

// Exports (M7): one row per requested timeline render. State transitions are
// owned by the media worker (queued → running → complete|failed); the row is
// created together with its media job so a queued export always has a job.

type Export struct {
	ID, WorkspaceID, ProjectID, TimelineID string
	Preset, State, Error                   string
	AssetID, VersionID                     string
	CreatedAt, UpdatedAt                   Time
}

const exportCols = `id, workspace_id, project_id, timeline_id, preset, state, error,
       asset_id, version_id, created_at, updated_at`

// CreateExport inserts the row and enqueues the render job atomically.
func (s *Store) CreateExport(ctx context.Context, e *Export) error {
	e.ID = ids.New("exp")
	e.State = "queued"
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := tx.QueryRow(ctx, `
		INSERT INTO exports (id, workspace_id, project_id, timeline_id, preset)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING created_at, updated_at`,
		e.ID, e.WorkspaceID, e.ProjectID, e.TimelineID, e.Preset).
		Scan(&e.CreatedAt, &e.UpdatedAt); err != nil {
		return err
	}
	if err := queue.EnqueueMediaJob(ctx, tx, e.WorkspaceID, "export",
		map[string]string{"export_id": e.ID}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) ListExports(ctx context.Context, timelineID string) ([]*Export, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+exportCols+` FROM exports
		WHERE timeline_id = $1
		ORDER BY created_at DESC
		LIMIT 50`, timelineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Export
	for rows.Next() {
		e := &Export{}
		if err := rows.Scan(&e.ID, &e.WorkspaceID, &e.ProjectID, &e.TimelineID,
			&e.Preset, &e.State, &e.Error, &e.AssetID, &e.VersionID,
			&e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
