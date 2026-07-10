package store

import (
	"context"

	"github.com/awm33/iris/backend/internal/ids"
	"github.com/awm33/iris/backend/internal/queue"
)

// Transcriptions (M7): lifecycle rows for transcribe media jobs — the
// result is caption ops on the timeline doc, this row is only what the UI
// polls (exports' pattern; see export.go).

type Transcription struct {
	ID, WorkspaceID, ProjectID, TimelineID string
	State, Error                           string
	SegmentCount                           int32
	CreatedAt, UpdatedAt                   Time
}

const transcriptionCols = `id, workspace_id, project_id, timeline_id, state, error,
       segment_count, created_at, updated_at`

// CreateTranscription inserts the row and enqueues the job atomically.
func (s *Store) CreateTranscription(ctx context.Context, t *Transcription) error {
	t.ID = ids.New("trs")
	t.State = "queued"
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := tx.QueryRow(ctx, `
		INSERT INTO transcriptions (id, workspace_id, project_id, timeline_id)
		VALUES ($1, $2, $3, $4)
		RETURNING created_at, updated_at`,
		t.ID, t.WorkspaceID, t.ProjectID, t.TimelineID).
		Scan(&t.CreatedAt, &t.UpdatedAt); err != nil {
		return err
	}
	if err := queue.EnqueueMediaJob(ctx, tx, t.WorkspaceID, "transcribe",
		map[string]string{"transcription_id": t.ID}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) ListTranscriptions(ctx context.Context, timelineID string) ([]*Transcription, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+transcriptionCols+` FROM transcriptions
		WHERE timeline_id = $1
		ORDER BY created_at DESC
		LIMIT 50`, timelineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Transcription
	for rows.Next() {
		t := &Transcription{}
		if err := rows.Scan(&t.ID, &t.WorkspaceID, &t.ProjectID, &t.TimelineID,
			&t.State, &t.Error, &t.SegmentCount, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
