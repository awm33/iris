package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/awm33/iris/backend/internal/ids"
)

// Story domain: scenes/sets/views/characters/shots/takes as relational rows
// (schema from migration 0001). Deliberately CRUD, not op-log documents —
// the doc runtime debuts with the canvas (M4) where it's unavoidable; story
// structure stays relational until collab requires otherwise (plan §M3 note).

type Scene struct {
	ID, ProjectID, Name, StyleNotes string
	Position                        int32
	Model3D                         *AssetRefJSON
	CreatedAt, UpdatedAt            Time
}

type ViewRow struct {
	ID, SceneID, Name string
	Plate             AssetRefJSON
	Position          int32
}

type CharacterRow struct {
	ID, WorkspaceID, ProjectID, Name string
	Refs                             []CharacterRefJSON
	CreatedAt, UpdatedAt             Time
}

type CharacterRefJSON struct {
	Role  string       `json:"role"`
	Asset AssetRefJSON `json:"asset"`
}

type AssetRefJSON struct {
	AssetID   string `json:"asset_id"`
	VersionID string `json:"version_id,omitempty"`
}

type ShotRow struct {
	ID, SceneID, Description string
	Position                 int32
	DurationTargetS          float64
	ViewID, SelectedTakeID   string
	SelectedTakeVersionID    string
	CastIDs                  []string
	ContinuityStale, Pinned  bool
	TakeCount                int32
	CreatedAt, UpdatedAt     Time
}

type TakeRow struct {
	ID, ShotID, JobID, VersionID, Quality string
	Starred                               bool
	Recipe                                json.RawMessage
	CreatedAt                             Time
}

// ── Scenes & views ────────────────────────────────────────────────────────────

func (s *Store) CreateScene(ctx context.Context, projectID, name string) (*Scene, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	sc := &Scene{}
	if err := tx.QueryRow(ctx, `
		INSERT INTO scenes (id, project_id, name, position)
		VALUES ($1, $2, $3,
		        COALESCE((SELECT max(position)+1 FROM scenes WHERE project_id = $2), 0))
		RETURNING id, project_id, name, position, style_notes, created_at, updated_at`,
		ids.New("scn"), projectID, name).
		Scan(&sc.ID, &sc.ProjectID, &sc.Name, &sc.Position, &sc.StyleNotes, &sc.CreatedAt, &sc.UpdatedAt); err != nil {
		return nil, err
	}
	// Every scene owns a Set from birth (HLD: the Set is the scene's world).
	if _, err := tx.Exec(ctx,
		`INSERT INTO sets (id, scene_id) VALUES ($1, $2)`, ids.New("set"), sc.ID); err != nil {
		return nil, err
	}
	return sc, tx.Commit(ctx)
}

func (s *Store) ListScenes(ctx context.Context, projectID string) ([]*Scene, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, project_id, name, position, style_notes, created_at, updated_at
		FROM scenes WHERE project_id = $1 ORDER BY position, created_at`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Scene
	for rows.Next() {
		sc := &Scene{}
		if err := rows.Scan(&sc.ID, &sc.ProjectID, &sc.Name, &sc.Position, &sc.StyleNotes,
			&sc.CreatedAt, &sc.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

func (s *Store) GetScene(ctx context.Context, id string) (*Scene, []*ViewRow, []*ShotRow, error) {
	// One REPEATABLE READ snapshot: scene, views, and shots must be mutually
	// consistent (a concurrent delete between queries would tear the result).
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, nil, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	sc := &Scene{}
	var model3d []byte
	err = tx.QueryRow(ctx, `
		SELECT sc.id, sc.project_id, sc.name, sc.position, sc.style_notes,
		       sc.created_at, sc.updated_at, st.model3d_ref
		FROM scenes sc JOIN sets st ON st.scene_id = sc.id
		WHERE sc.id = $1`, id).
		Scan(&sc.ID, &sc.ProjectID, &sc.Name, &sc.Position, &sc.StyleNotes,
			&sc.CreatedAt, &sc.UpdatedAt, &model3d)
	if err != nil {
		return nil, nil, nil, wrapNotFound(err)
	}
	if len(model3d) > 0 {
		ref := &AssetRefJSON{}
		if json.Unmarshal(model3d, ref) == nil && ref.AssetID != "" {
			sc.Model3D = ref
		}
	}

	views, err := s.listViews(ctx, tx, sc.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	shots, err := s.listShots(ctx, tx, sc.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	return sc, views, shots, tx.Commit(ctx)
}

func (s *Store) UpdateScene(ctx context.Context, id string, name, styleNotes *string, position *int32) (*Scene, error) {
	sc := &Scene{}
	err := s.pool.QueryRow(ctx, `
		UPDATE scenes SET
			name = COALESCE($2, name),
			style_notes = COALESCE($3, style_notes),
			position = COALESCE($4, position),
			updated_at = now()
		WHERE id = $1
		RETURNING id, project_id, name, position, style_notes, created_at, updated_at`,
		id, name, styleNotes, position).
		Scan(&sc.ID, &sc.ProjectID, &sc.Name, &sc.Position, &sc.StyleNotes, &sc.CreatedAt, &sc.UpdatedAt)
	return sc, wrapNotFound(err)
}

func (s *Store) AddView(ctx context.Context, sceneID, name string, plate AssetRefJSON) (*ViewRow, error) {
	plateJSON, _ := json.Marshal(plate)
	v := &ViewRow{}
	var plateRaw []byte
	err := s.pool.QueryRow(ctx, `
		INSERT INTO views (id, set_id, name, plate_ref, position)
		SELECT $1, st.id, $2, $3,
		       COALESCE((SELECT max(v.position)+1 FROM views v WHERE v.set_id = st.id), 0)
		FROM sets st WHERE st.scene_id = $4
		RETURNING id, $4::text, name, plate_ref, position`,
		ids.New("view"), name, plateJSON, sceneID).
		Scan(&v.ID, &v.SceneID, &v.Name, &plateRaw, &v.Position)
	if err != nil {
		return nil, wrapNotFound(err)
	}
	_ = json.Unmarshal(plateRaw, &v.Plate)
	return v, nil
}

func (s *Store) RemoveView(ctx context.Context, id string) error {
	// Shots referencing the view keep working (view_id dangles by design —
	// the shot's framing intent outlives the catalog entry).
	tag, err := s.pool.Exec(ctx, `DELETE FROM views WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) listViews(ctx context.Context, q querier, sceneID string) ([]*ViewRow, error) {
	rows, err := q.Query(ctx, `
		SELECT v.id, $1::text, v.name, v.plate_ref, v.position
		FROM views v JOIN sets st ON st.id = v.set_id
		WHERE st.scene_id = $1 ORDER BY v.position, v.id`, sceneID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ViewRow
	for rows.Next() {
		v := &ViewRow{}
		var plateRaw []byte
		if err := rows.Scan(&v.ID, &v.SceneID, &v.Name, &plateRaw, &v.Position); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(plateRaw, &v.Plate)
		out = append(out, v)
	}
	return out, rows.Err()
}

// ── Shots & takes ─────────────────────────────────────────────────────────────

func (s *Store) CreateShot(ctx context.Context, sceneID, description string, durationTargetS float64) (*ShotRow, error) {
	sh := &ShotRow{}
	var viewID, selected *string
	var duration *float64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO shots (id, scene_id, description, duration_target_s, position)
		VALUES ($1, $2, $3, NULLIF($4, 0.0),
		        COALESCE((SELECT max(position)+1 FROM shots WHERE scene_id = $2), 0))
		RETURNING id, scene_id, position, description, duration_target_s, view_id,
		          cast_ids, selected_take_id, continuity_stale, pinned, created_at, updated_at`,
		ids.New("sht"), sceneID, description, durationTargetS).
		Scan(&sh.ID, &sh.SceneID, &sh.Position, &sh.Description, &duration, &viewID,
			&sh.CastIDs, &selected, &sh.ContinuityStale, &sh.Pinned, &sh.CreatedAt, &sh.UpdatedAt)
	if err != nil {
		return nil, err
	}
	derefShot(sh, duration, viewID, selected)
	return sh, nil
}

func (s *Store) UpdateShot(ctx context.Context, id string, description *string, durationTargetS *float64, viewID *string, castIDs []string, setCast bool, position *int32, pinned *bool, clearView bool) (*ShotRow, error) {
	sh := &ShotRow{}
	var vID, selected *string
	var duration *float64
	var cast any
	if setCast {
		if castIDs == nil {
			// proto3 empty repeated arrives as nil; pgx encodes nil as SQL
			// NULL, which COALESCE would swallow — "clear the cast" must
			// actually clear it.
			castIDs = []string{}
		}
		cast = castIDs
	}
	// duration: omitted = keep; 0 = clear (NULL, matching CreateShot).
	err := s.pool.QueryRow(ctx, `
		UPDATE shots SET
			description = COALESCE($2, description),
			duration_target_s = CASE WHEN $3::float8 IS NULL THEN duration_target_s
			                         ELSE NULLIF($3, 0.0) END,
			view_id = CASE WHEN $8 THEN NULL
			               ELSE COALESCE(NULLIF($4, ''), view_id) END,
			cast_ids = COALESCE($5, cast_ids),
			position = COALESCE($6, position),
			pinned = COALESCE($7, pinned),
			updated_at = now()
		WHERE id = $1
		RETURNING id, scene_id, position, description, duration_target_s, view_id,
		          cast_ids, selected_take_id, continuity_stale, pinned, created_at, updated_at`,
		id, description, durationTargetS, viewID, cast, position, pinned, clearView).
		Scan(&sh.ID, &sh.SceneID, &sh.Position, &sh.Description, &duration, &vID,
			&sh.CastIDs, &selected, &sh.ContinuityStale, &sh.Pinned, &sh.CreatedAt, &sh.UpdatedAt)
	if err != nil {
		return nil, wrapNotFound(err)
	}
	derefShot(sh, duration, vID, selected)
	return sh, nil
}

// CharactersExist reports whether every id names a character in the workspace.
func (s *Store) CharactersExist(ctx context.Context, workspaceID string, ids []string) (bool, error) {
	if len(ids) == 0 {
		return true, nil
	}
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(DISTINCT id) FROM characters
		WHERE workspace_id = $1 AND id = ANY($2)`, workspaceID, ids).Scan(&n)
	if err != nil {
		return false, err
	}
	uniq := map[string]bool{}
	for _, id := range ids {
		uniq[id] = true
	}
	return n == len(uniq), nil
}

// ShotProjectID resolves a shot to its owning project (cross-project target
// checks in CreateJob).
func (s *Store) ShotProjectID(ctx context.Context, shotID string) (string, error) {
	var projectID string
	err := s.pool.QueryRow(ctx, `
		SELECT sc.project_id FROM shots sh JOIN scenes sc ON sc.id = sh.scene_id
		WHERE sh.id = $1`, shotID).Scan(&projectID)
	return projectID, wrapNotFound(err)
}

func (s *Store) DeleteShot(ctx context.Context, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`UPDATE shots SET selected_take_id = NULL WHERE id = $1`, id); err != nil {
		return err
	}
	// Lineage edges to the deleted takes would dangle otherwise; the asset
	// versions themselves survive in the library (deliberate).
	if _, err := tx.Exec(ctx, `
		DELETE FROM asset_links
		WHERE role = 'used_in_take'
		  AND to_entity_id IN (SELECT id FROM takes WHERE shot_id = $1)`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM takes WHERE shot_id = $1`, id); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM shots WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

// querier abstracts pool vs tx for the scene-snapshot reads.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func (s *Store) listShots(ctx context.Context, q querier, sceneID string) ([]*ShotRow, error) {
	rows, err := q.Query(ctx, `
		SELECT sh.id, sh.scene_id, sh.position, sh.description, sh.duration_target_s,
		       sh.view_id, sh.cast_ids, sh.selected_take_id, sh.continuity_stale, sh.pinned,
		       sh.created_at, sh.updated_at,
		       (SELECT count(*) FROM takes t WHERE t.shot_id = sh.id) AS take_count,
		       COALESCE(sel.version_id, '') AS selected_version
		FROM shots sh
		LEFT JOIN takes sel ON sel.id = sh.selected_take_id
		WHERE sh.scene_id = $1 ORDER BY sh.position, sh.created_at`, sceneID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ShotRow
	for rows.Next() {
		sh := &ShotRow{}
		var viewID, selected *string
		var duration *float64
		if err := rows.Scan(&sh.ID, &sh.SceneID, &sh.Position, &sh.Description, &duration,
			&viewID, &sh.CastIDs, &selected, &sh.ContinuityStale, &sh.Pinned,
			&sh.CreatedAt, &sh.UpdatedAt, &sh.TakeCount, &sh.SelectedTakeVersionID); err != nil {
			return nil, err
		}
		derefShot(sh, duration, viewID, selected)
		out = append(out, sh)
	}
	return out, rows.Err()
}

func derefShot(sh *ShotRow, duration *float64, viewID, selected *string) {
	if duration != nil {
		sh.DurationTargetS = *duration
	}
	if viewID != nil {
		sh.ViewID = *viewID
	}
	if selected != nil {
		sh.SelectedTakeID = *selected
	}
}

func (s *Store) ListTakes(ctx context.Context, shotID string) ([]*TakeRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, shot_id, COALESCE(job_id,''), COALESCE(version_id,''), quality, starred, recipe, created_at
		FROM takes WHERE shot_id = $1 ORDER BY created_at`, shotID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*TakeRow
	for rows.Next() {
		t := &TakeRow{}
		if err := rows.Scan(&t.ID, &t.ShotID, &t.JobID, &t.VersionID, &t.Quality,
			&t.Starred, &t.Recipe, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SelectTake sets the shot's selected take (must belong to the shot).
// CHANGING the selection marks downstream shots in the scene that already
// have takes ⚠ continuity_stale — their content was generated against the
// previous upstream pick (W3: the carry input just moved under them).
// Re-selecting the same take propagates nothing.
func (s *Store) SelectTake(ctx context.Context, shotID, takeID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var sceneID string
	var position int32
	var changed bool
	err = tx.QueryRow(ctx, `
		WITH old AS (SELECT selected_take_id FROM shots WHERE id = $1)
		UPDATE shots SET selected_take_id = $2, updated_at = now()
		WHERE id = $1 AND EXISTS (SELECT 1 FROM takes WHERE id = $2 AND shot_id = $1)
		RETURNING scene_id, position,
		          (SELECT selected_take_id FROM old) IS DISTINCT FROM $2`,
		shotID, takeID).Scan(&sceneID, &position, &changed)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if changed {
		if _, err := tx.Exec(ctx, `
			UPDATE shots SET continuity_stale = true, updated_at = now()
			WHERE scene_id = $1 AND position > $2 AND NOT continuity_stale
			  AND EXISTS (SELECT 1 FROM takes WHERE takes.shot_id = shots.id)`,
			sceneID, position); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) GetShot(ctx context.Context, id string) (*ShotRow, error) {
	sh := &ShotRow{}
	var viewID, selected *string
	var duration *float64
	err := s.pool.QueryRow(ctx, `
		SELECT sh.id, sh.scene_id, sh.position, sh.description, sh.duration_target_s,
		       sh.view_id, sh.cast_ids, sh.selected_take_id, sh.continuity_stale, sh.pinned,
		       sh.created_at, sh.updated_at,
		       (SELECT count(*) FROM takes t WHERE t.shot_id = sh.id),
		       COALESCE(sel.version_id, '')
		FROM shots sh
		LEFT JOIN takes sel ON sel.id = sh.selected_take_id
		WHERE sh.id = $1`, id).
		Scan(&sh.ID, &sh.SceneID, &sh.Position, &sh.Description, &duration,
			&viewID, &sh.CastIDs, &selected, &sh.ContinuityStale, &sh.Pinned,
			&sh.CreatedAt, &sh.UpdatedAt, &sh.TakeCount, &sh.SelectedTakeVersionID)
	if err != nil {
		return nil, wrapNotFound(err)
	}
	derefShot(sh, duration, viewID, selected)
	return sh, nil
}

// ── Characters ────────────────────────────────────────────────────────────────

func (s *Store) CreateCharacter(ctx context.Context, workspaceID, projectID, name string) (*CharacterRow, error) {
	c := &CharacterRow{}
	var refsRaw []byte
	var pID *string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO characters (id, workspace_id, project_id, name)
		VALUES ($1, $2, NULLIF($3,''), $4)
		RETURNING id, workspace_id, project_id, name, refs, created_at, updated_at`,
		ids.New("chr"), workspaceID, projectID, name).
		Scan(&c.ID, &c.WorkspaceID, &pID, &c.Name, &refsRaw, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if pID != nil {
		c.ProjectID = *pID
	}
	_ = json.Unmarshal(refsRaw, &c.Refs)
	return c, nil
}

func (s *Store) ListCharacters(ctx context.Context, workspaceID, projectID string) ([]*CharacterRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, workspace_id, COALESCE(project_id,''), name, refs, created_at, updated_at
		FROM characters
		WHERE workspace_id = $1 AND (project_id IS NULL OR $2 = '' OR project_id = $2)
		ORDER BY created_at`, workspaceID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*CharacterRow
	for rows.Next() {
		c := &CharacterRow{}
		var refsRaw []byte
		if err := rows.Scan(&c.ID, &c.WorkspaceID, &c.ProjectID, &c.Name, &refsRaw,
			&c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(refsRaw, &c.Refs)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) UpdateCharacter(ctx context.Context, id string, name *string) (*CharacterRow, error) {
	c := &CharacterRow{}
	var refsRaw []byte
	var pID *string
	err := s.pool.QueryRow(ctx, `
		UPDATE characters SET name = COALESCE($2, name), updated_at = now()
		WHERE id = $1
		RETURNING id, workspace_id, project_id, name, refs, created_at, updated_at`,
		id, name).
		Scan(&c.ID, &c.WorkspaceID, &pID, &c.Name, &refsRaw, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, wrapNotFound(err)
	}
	if pID != nil {
		c.ProjectID = *pID
	}
	_ = json.Unmarshal(refsRaw, &c.Refs)
	return c, nil
}

// ErrRefLimit distinguishes "bundle full" from NotFound in AddCharacterRef.
var ErrRefLimit = errors.New("character reference limit reached")

const maxCharacterRefs = 50

// AddCharacterRef appends a reference to the character's bundle (atomic
// jsonb append — no read-modify-write race), bounded at maxCharacterRefs.
func (s *Store) AddCharacterRef(ctx context.Context, characterID string, ref CharacterRefJSON) (*CharacterRow, error) {
	refJSON, _ := json.Marshal(ref)
	c := &CharacterRow{}
	var refsRaw []byte
	var pID *string
	err := s.pool.QueryRow(ctx, `
		UPDATE characters SET refs = refs || $2::jsonb, updated_at = now()
		WHERE id = $1 AND jsonb_array_length(refs) < $3
		RETURNING id, workspace_id, project_id, name, refs, created_at, updated_at`,
		characterID, refJSON, maxCharacterRefs).
		Scan(&c.ID, &c.WorkspaceID, &pID, &c.Name, &refsRaw, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if e := s.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM characters WHERE id = $1)`, characterID).Scan(&exists); e == nil && exists {
			return nil, ErrRefLimit
		}
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if pID != nil {
		c.ProjectID = *pID
	}
	_ = json.Unmarshal(refsRaw, &c.Refs)
	return c, nil
}

// RemoveCharacterRef drops every ref matching (role, asset_id).
func (s *Store) RemoveCharacterRef(ctx context.Context, characterID, role, assetID string) (*CharacterRow, error) {
	c := &CharacterRow{}
	var refsRaw []byte
	var pID *string
	err := s.pool.QueryRow(ctx, `
		UPDATE characters SET
			refs = COALESCE(
				(SELECT jsonb_agg(e) FROM jsonb_array_elements(refs) e
				 WHERE NOT (e->>'role' = $2 AND e->'asset'->>'asset_id' = $3)),
				'[]'::jsonb),
			updated_at = now()
		WHERE id = $1
		RETURNING id, workspace_id, project_id, name, refs, created_at, updated_at`,
		characterID, role, assetID).
		Scan(&c.ID, &c.WorkspaceID, &pID, &c.Name, &refsRaw, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, wrapNotFound(err)
	}
	if pID != nil {
		c.ProjectID = *pID
	}
	_ = json.Unmarshal(refsRaw, &c.Refs)
	return c, nil
}

// VersionBelongsToAsset validates a client-supplied (asset, version) pair —
// plate_ref/character refs store version ids without FKs.
func (s *Store) VersionBelongsToAsset(ctx context.Context, versionID, assetID string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM asset_versions WHERE id = $1 AND asset_id = $2)`,
		versionID, assetID).Scan(&ok)
	return ok, err
}
