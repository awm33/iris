package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/awm33/iris/backend/internal/ids"
)

// Timelines mirror canvases: op-log docs with head_seq as the append lock
// (see canvas.go for the concurrency argument — conditional UPDATE keeps
// seqs gapless with a single effective writer per doc).

type Timeline struct {
	ID, WorkspaceID, ProjectID, DocID, Name string
	FPS                                     int32
	HeadSeq                                 int64
	CreatedAt, UpdatedAt                    Time
}

func (s *Store) CreateTimeline(ctx context.Context, t *Timeline) error {
	t.ID = ids.New("tl")
	t.DocID = ids.New("doc")
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `INSERT INTO docs (id, project_id, kind) VALUES ($1, $2, 'timeline')`,
		t.DocID, t.ProjectID); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO timelines (id, workspace_id, project_id, doc_id, name, fps)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING head_seq, created_at, updated_at`,
		t.ID, t.WorkspaceID, t.ProjectID, t.DocID, t.Name, t.FPS).
		Scan(&t.HeadSeq, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const timelineCols = `id, workspace_id, project_id, doc_id, name, fps, head_seq, created_at, updated_at`

func scanTimeline(row pgx.Row) (*Timeline, error) {
	t := &Timeline{}
	err := row.Scan(&t.ID, &t.WorkspaceID, &t.ProjectID, &t.DocID, &t.Name,
		&t.FPS, &t.HeadSeq, &t.CreatedAt, &t.UpdatedAt)
	return t, wrapNotFound(err)
}

func (s *Store) GetTimeline(ctx context.Context, id string) (*Timeline, error) {
	return scanTimeline(s.pool.QueryRow(ctx,
		`SELECT `+timelineCols+` FROM timelines WHERE id = $1`, id))
}

func (s *Store) ListTimelines(ctx context.Context, workspaceID, projectID string) ([]*Timeline, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+timelineCols+` FROM timelines
		WHERE workspace_id = $1 AND project_id = $2
		ORDER BY created_at DESC`, workspaceID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Timeline
	for rows.Next() {
		t, err := scanTimeline(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// AppendTimelineOps: identical contract to AppendCanvasOps — see canvas.go.
func (s *Store) AppendTimelineOps(ctx context.Context, timelineID string, baseSeq int64, actorID string, payloads [][]byte) (int64, error) {
	if len(payloads) == 0 {
		return 0, errors.New("no ops to append")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var head int64
	var docID string
	err = tx.QueryRow(ctx, `
		UPDATE timelines SET head_seq = head_seq + $3, updated_at = now()
		WHERE id = $1 AND head_seq = $2
		RETURNING head_seq, doc_id`,
		timelineID, baseSeq, len(payloads)).Scan(&head, &docID)
	if errors.Is(err, pgx.ErrNoRows) {
		var cur int64
		if err2 := tx.QueryRow(ctx,
			`SELECT head_seq FROM timelines WHERE id = $1`, timelineID).Scan(&cur); err2 != nil {
			return 0, wrapNotFound(err2)
		}
		return cur, ErrSeqConflict
	}
	if err != nil {
		return 0, err
	}
	rows := make([][]any, len(payloads))
	for i, p := range payloads {
		rows[i] = []any{docID, baseSeq + int64(i) + 1, actorID, p}
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"doc_ops"},
		[]string{"doc_id", "seq", "actor_id", "op"}, pgx.CopyFromRows(rows)); err != nil {
		return 0, err
	}
	return head, tx.Commit(ctx)
}

func (s *Store) DeleteTimeline(ctx context.Context, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var docID string
	if err := tx.QueryRow(ctx,
		`DELETE FROM timelines WHERE id = $1 RETURNING doc_id`, id).Scan(&docID); err != nil {
		return wrapNotFound(err)
	}
	for _, q := range []string{
		`DELETE FROM doc_ops WHERE doc_id = $1`,
		`DELETE FROM doc_snapshots WHERE doc_id = $1`,
		`DELETE FROM docs WHERE id = $1`,
	} {
		if _, err := tx.Exec(ctx, q, docID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
