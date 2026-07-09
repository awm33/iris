package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/awm33/iris/backend/internal/ids"
)

// ErrSeqConflict: the client's base_seq is stale — another writer appended
// first. The client must refetch ops and replay before retrying.
var ErrSeqConflict = errors.New("base_seq is stale — refetch and replay")

type Canvas struct {
	ID, WorkspaceID, ProjectID, DocID, Name string
	Width, Height                           int32
	HeadSeq                                 int64
	CreatedAt, UpdatedAt                    Time
}

type DocOp struct {
	Seq     int64
	ActorID string
	Payload []byte
}

// CreateCanvas inserts the doc (op-log identity) and the canvas metadata in
// one transaction.
func (s *Store) CreateCanvas(ctx context.Context, c *Canvas) error {
	c.ID = ids.New("cnv")
	c.DocID = ids.New("doc")
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO docs (id, project_id, kind) VALUES ($1, $2, 'canvas')`,
		c.DocID, c.ProjectID); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO canvases (id, workspace_id, project_id, doc_id, name, width, height)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING head_seq, created_at, updated_at`,
		c.ID, c.WorkspaceID, c.ProjectID, c.DocID, c.Name, c.Width, c.Height).
		Scan(&c.HeadSeq, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const canvasCols = `id, workspace_id, project_id, doc_id, name, width, height, head_seq, created_at, updated_at`

func scanCanvas(row pgx.Row) (*Canvas, error) {
	c := &Canvas{}
	err := row.Scan(&c.ID, &c.WorkspaceID, &c.ProjectID, &c.DocID, &c.Name,
		&c.Width, &c.Height, &c.HeadSeq, &c.CreatedAt, &c.UpdatedAt)
	return c, wrapNotFound(err)
}

func (s *Store) GetCanvas(ctx context.Context, id string) (*Canvas, error) {
	return scanCanvas(s.pool.QueryRow(ctx,
		`SELECT `+canvasCols+` FROM canvases WHERE id = $1`, id))
}

func (s *Store) ListCanvases(ctx context.Context, workspaceID, projectID string) ([]*Canvas, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+canvasCols+` FROM canvases
		WHERE workspace_id = $1 AND project_id = $2
		ORDER BY created_at DESC`, workspaceID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Canvas
	for rows.Next() {
		c, err := scanCanvas(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListCanvasOps returns ops with seq > afterSeq, ascending, capped at limit.
func (s *Store) ListCanvasOps(ctx context.Context, docID string, afterSeq int64, limit int) ([]*DocOp, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT seq, actor_id, op FROM doc_ops
		WHERE doc_id = $1 AND seq > $2
		ORDER BY seq LIMIT $3`, docID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*DocOp
	for rows.Next() {
		op := &DocOp{}
		if err := rows.Scan(&op.Seq, &op.ActorID, &op.Payload); err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

// AppendCanvasOps assigns seqs baseSeq+1..baseSeq+n and inserts the ops.
// The conditional head_seq bump is the whole concurrency story: it only
// succeeds when baseSeq is current, so seqs stay gapless and there is a
// single effective writer per doc without advisory locks.
func (s *Store) AppendCanvasOps(ctx context.Context, canvasID string, baseSeq int64, actorID string, payloads [][]byte) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var head int64
	var docID string
	err = tx.QueryRow(ctx, `
		UPDATE canvases SET head_seq = head_seq + $3, updated_at = now()
		WHERE id = $1 AND head_seq = $2
		RETURNING head_seq, doc_id`,
		canvasID, baseSeq, len(payloads)).Scan(&head, &docID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Distinguish a stale base_seq from a missing canvas.
		var cur int64
		if err2 := s.pool.QueryRow(ctx,
			`SELECT head_seq FROM canvases WHERE id = $1`, canvasID).Scan(&cur); err2 != nil {
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

func (s *Store) RenameCanvas(ctx context.Context, id, name string) (*Canvas, error) {
	return scanCanvas(s.pool.QueryRow(ctx, `
		UPDATE canvases SET name = $2, updated_at = now()
		WHERE id = $1 RETURNING `+canvasCols, id, name))
}

func (s *Store) DeleteCanvas(ctx context.Context, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var docID string
	if err := tx.QueryRow(ctx,
		`DELETE FROM canvases WHERE id = $1 RETURNING doc_id`, id).Scan(&docID); err != nil {
		return wrapNotFound(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM doc_ops WHERE doc_id = $1`, docID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM doc_snapshots WHERE doc_id = $1`, docID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM docs WHERE id = $1`, docID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
