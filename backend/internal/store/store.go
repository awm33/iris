// Package store is the Postgres data layer (pgx). Queries are hand-written
// for M1; sqlc adoption is planned once the query surface stabilizes
// (TDD §3.1 conventions).
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/awm33/iris/backend/internal/ids"
	"github.com/awm33/iris/backend/internal/queue"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	pool *pgxpool.Pool
}

func New(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("postgres ping: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

// ── Dev seed (auth v0) ────────────────────────────────────────────────────────

const DevWorkspaceID = "ws_dev"

// EnsureDevWorkspace creates the single-user dev workspace if absent (auth v0:
// every request operates as this workspace's owner until a real IdP lands).
func (s *Store) EnsureDevWorkspace(ctx context.Context, ownerEmail string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO workspaces (id, name) VALUES ($1, 'Dev Workspace')
		ON CONFLICT (id) DO NOTHING`, DevWorkspaceID)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO members (id, workspace_id, email, role)
		VALUES ($1, $2, $3, 'owner')
		ON CONFLICT (workspace_id, email) DO NOTHING`,
		ids.New("mem"), DevWorkspaceID, ownerEmail)
	return err
}

// ── Workspaces & projects ─────────────────────────────────────────────────────

type Workspace struct {
	ID, Name             string
	CreatedAt, UpdatedAt Time
}

type Project struct {
	ID, WorkspaceID, Name, Description string
	CreatedAt, UpdatedAt               Time
}

func (s *Store) GetWorkspace(ctx context.Context, id string) (*Workspace, error) {
	w := &Workspace{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, created_at, updated_at FROM workspaces WHERE id = $1`, id).
		Scan(&w.ID, &w.Name, &w.CreatedAt, &w.UpdatedAt)
	return w, wrapNotFound(err)
}

func (s *Store) CreateProject(ctx context.Context, workspaceID, name, description string) (*Project, error) {
	p := &Project{}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO projects (id, workspace_id, name, description)
		VALUES ($1, $2, $3, $4)
		RETURNING id, workspace_id, name, description, created_at, updated_at`,
		ids.New("prj"), workspaceID, name, description).
		Scan(&p.ID, &p.WorkspaceID, &p.Name, &p.Description, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

func (s *Store) ListProjects(ctx context.Context, workspaceID string) ([]*Project, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, workspace_id, name, description, created_at, updated_at
		FROM projects WHERE workspace_id = $1 AND archived_at IS NULL
		ORDER BY created_at DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Project
	for rows.Next() {
		p := &Project{}
		if err := rows.Scan(&p.ID, &p.WorkspaceID, &p.Name, &p.Description, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetProject(ctx context.Context, id string) (*Project, error) {
	p := &Project{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, workspace_id, name, description, created_at, updated_at
		FROM projects WHERE id = $1 AND archived_at IS NULL`, id).
		Scan(&p.ID, &p.WorkspaceID, &p.Name, &p.Description, &p.CreatedAt, &p.UpdatedAt)
	return p, wrapNotFound(err)
}

func (s *Store) UpdateProject(ctx context.Context, id string, name, description *string) (*Project, error) {
	p := &Project{}
	err := s.pool.QueryRow(ctx, `
		UPDATE projects SET
			name = COALESCE($2, name),
			description = COALESCE($3, description),
			updated_at = now()
		WHERE id = $1 AND archived_at IS NULL
		RETURNING id, workspace_id, name, description, created_at, updated_at`,
		id, name, description).
		Scan(&p.ID, &p.WorkspaceID, &p.Name, &p.Description, &p.CreatedAt, &p.UpdatedAt)
	return p, wrapNotFound(err)
}

func (s *Store) ArchiveProject(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE projects SET archived_at = now() WHERE id = $1 AND archived_at IS NULL`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ── Uploads & assets ──────────────────────────────────────────────────────────

type PendingUpload struct {
	ID, WorkspaceID, ProjectID       string
	Filename, ContentType, ObjectKey string
	SizeBytes                        int64
}

func (s *Store) CreatePendingUpload(ctx context.Context, u *PendingUpload) error {
	u.ID = ids.New("upl")
	u.ObjectKey = "uploads/" + u.ID
	_, err := s.pool.Exec(ctx, `
		INSERT INTO pending_uploads (id, workspace_id, project_id, filename, content_type, size_bytes, object_key)
		VALUES ($1, $2, NULLIF($3,''), $4, $5, $6, $7)`,
		u.ID, u.WorkspaceID, u.ProjectID, u.Filename, u.ContentType, u.SizeBytes, u.ObjectKey)
	return err
}

func (s *Store) TakePendingUpload(ctx context.Context, id string) (*PendingUpload, error) {
	u := &PendingUpload{}
	var projectID *string
	err := s.pool.QueryRow(ctx, `
		DELETE FROM pending_uploads WHERE id = $1
		RETURNING id, workspace_id, project_id, filename, content_type, size_bytes, object_key`, id).
		Scan(&u.ID, &u.WorkspaceID, &projectID, &u.Filename, &u.ContentType, &u.SizeBytes, &u.ObjectKey)
	if projectID != nil {
		u.ProjectID = *projectID
	}
	return u, wrapNotFound(err)
}

type Asset struct {
	ID, WorkspaceID, ProjectID, Kind, Name, HeadVersionID string
	Tags                                                  []string
	CreatedAt, UpdatedAt                                  Time
}

type AssetVersion struct {
	ID, AssetID, SHA256, ContentType string
	SizeBytes                        int64
	Width, Height                    int32
	DurationS, FPS                   float64
	CreatedAt                        Time
}

// CreateAssetWithVersion inserts the asset identity + first version + head
// pointer in one transaction. enqueueProbe adds a media probe job to the same
// transaction (NOTIFY fires on commit — job and version land atomically).
// versionMeta (optional) lands in the version's meta column in the same
// transaction — attribution must not be a lossy afterthought.
func (s *Store) CreateAssetWithVersion(ctx context.Context, a *Asset, v *AssetVersion, enqueueProbe bool, versionMeta map[string]any) error {
	a.ID = ids.New("ast")
	v.ID = ids.New("astv")
	v.AssetID = a.ID
	var metaJSON []byte
	if versionMeta != nil {
		var err error
		if metaJSON, err = json.Marshal(versionMeta); err != nil {
			return err
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO assets (id, workspace_id, project_id, kind, name)
		VALUES ($1, $2, NULLIF($3,''), $4, $5)`,
		a.ID, a.WorkspaceID, a.ProjectID, a.Kind, a.Name); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO asset_versions (id, asset_id, sha256, content_type, size_bytes, width, height, duration_s, fps, meta)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6,0), NULLIF($7,0), NULLIF($8,0.0), NULLIF($9,0.0), COALESCE($10::jsonb, '{}'::jsonb))`,
		v.ID, a.ID, v.SHA256, v.ContentType, v.SizeBytes, v.Width, v.Height, v.DurationS, v.FPS, metaJSON); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE assets SET head_version_id = $2 WHERE id = $1`, a.ID, v.ID); err != nil {
		return err
	}
	if enqueueProbe {
		if err := queue.EnqueueMediaJob(ctx, tx, a.WorkspaceID, "probe",
			map[string]string{"version_id": v.ID}); err != nil {
			return err
		}
	}
	a.HeadVersionID = v.ID
	return tx.Commit(ctx)
}

// FindStockAsset returns the asset already imported from a stock source
// (matched on version meta source/source_id within the project), or
// ErrNotFound. Makes stock imports idempotent — re-importing the same photo
// returns the existing asset instead of a duplicate library entry.
func (s *Store) FindStockAsset(ctx context.Context, workspaceID, projectID, source, sourceID string) (string, error) {
	var assetID string
	err := s.pool.QueryRow(ctx, `
		SELECT a.id FROM assets a
		JOIN asset_versions v ON v.asset_id = a.id
		WHERE a.workspace_id = $1 AND a.project_id IS NOT DISTINCT FROM NULLIF($2,'')
		  AND v.meta->>'source' = $3 AND v.meta->>'source_id' = $4
		ORDER BY a.created_at LIMIT 1`,
		workspaceID, projectID, source, sourceID).Scan(&assetID)
	return assetID, wrapNotFound(err)
}

func (s *Store) GetAsset(ctx context.Context, id string) (*Asset, []*AssetVersion, error) {
	a := &Asset{}
	var projectID, head *string
	err := s.pool.QueryRow(ctx, `
		SELECT id, workspace_id, project_id, kind, name, head_version_id, tags, created_at, updated_at
		FROM assets WHERE id = $1`, id).
		Scan(&a.ID, &a.WorkspaceID, &projectID, &a.Kind, &a.Name, &head, &a.Tags, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, nil, wrapNotFound(err)
	}
	if projectID != nil {
		a.ProjectID = *projectID
	}
	if head != nil {
		a.HeadVersionID = *head
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, asset_id, sha256, content_type, size_bytes,
		       COALESCE(width,0), COALESCE(height,0), COALESCE(duration_s,0), COALESCE(fps,0), created_at
		FROM asset_versions WHERE asset_id = $1 ORDER BY created_at DESC`, id)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var versions []*AssetVersion
	for rows.Next() {
		v := &AssetVersion{}
		if err := rows.Scan(&v.ID, &v.AssetID, &v.SHA256, &v.ContentType, &v.SizeBytes,
			&v.Width, &v.Height, &v.DurationS, &v.FPS, &v.CreatedAt); err != nil {
			return nil, nil, err
		}
		versions = append(versions, v)
	}
	return a, versions, rows.Err()
}

func (s *Store) ListAssets(ctx context.Context, workspaceID, projectID, kind, query string) ([]*Asset, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, workspace_id, COALESCE(project_id,''), kind, name, COALESCE(head_version_id,''), tags, created_at, updated_at
		FROM assets
		WHERE workspace_id = $1
		  AND ($2 = '' OR project_id = $2)
		  AND ($3 = '' OR kind = $3)
		  AND ($4 = '' OR name ILIKE '%' || $4 || '%')
		ORDER BY created_at DESC
		LIMIT 200`, workspaceID, projectID, kind, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Asset
	for rows.Next() {
		a := &Asset{}
		if err := rows.Scan(&a.ID, &a.WorkspaceID, &a.ProjectID, &a.Kind, &a.Name,
			&a.HeadVersionID, &a.Tags, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

type VersionObjectInfo struct {
	SHA256      string
	ContentType string
	PosterKey   string            // "" until the probe job has produced one
	DerivedKeys map[string]string // prep artifacts: proxy_key, filmstrip_key, first_frame_key, last_frame_key, waveform_key
}

func (s *Store) GetVersionObjectInfo(ctx context.Context, versionID string) (*VersionObjectInfo, error) {
	info := &VersionObjectInfo{DerivedKeys: map[string]string{}}
	var proxy, strip, first, last, wave string
	err := wrapNotFound(s.pool.QueryRow(ctx,
		`SELECT sha256, content_type, COALESCE(meta->>'poster_key', ''),
		        COALESCE(meta->>'proxy_key', ''), COALESCE(meta->>'filmstrip_key', ''),
		        COALESCE(meta->>'first_frame_key', ''), COALESCE(meta->>'last_frame_key', ''),
		        COALESCE(meta->>'waveform_key', '')
		 FROM asset_versions WHERE id = $1`, versionID).
		Scan(&info.SHA256, &info.ContentType, &info.PosterKey, &proxy, &strip, &first, &last, &wave))
	for k, v := range map[string]string{
		"proxy": proxy, "filmstrip": strip, "first_frame": first, "last_frame": last, "waveform": wave,
	} {
		if v != "" {
			info.DerivedKeys[k] = v
		}
	}
	return info, err
}

type AssetLink struct {
	FromVersionID, ToEntityID, Role string
}

func (s *Store) GetLineage(ctx context.Context, versionID string) (upstream, downstream []*AssetLink, err error) {
	rows, err := s.pool.Query(ctx, `
		SELECT from_version_id, to_entity_id, role FROM asset_links
		WHERE from_version_id = $1 OR to_entity_id = $1`, versionID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		l := &AssetLink{}
		if err := rows.Scan(&l.FromVersionID, &l.ToEntityID, &l.Role); err != nil {
			return nil, nil, err
		}
		if l.FromVersionID == versionID {
			downstream = append(downstream, l)
		} else {
			upstream = append(upstream, l)
		}
	}
	return upstream, downstream, rows.Err()
}

func wrapNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
