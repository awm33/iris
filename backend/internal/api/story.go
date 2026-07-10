package api

import (
	"context"
	"errors"
	"math"

	"connectrpc.com/connect"

	irisv1 "github.com/awm33/iris/backend/gen/iris/v1"
	"github.com/awm33/iris/backend/internal/store"
)

type StoryServer struct {
	Store *store.Store
}

// Input bounds, matching the generation surface's discipline.
const (
	maxNameLen      = 200
	maxLongTextLen  = 20_000
	maxDurationS    = 3600.0
	maxCastSize     = 20
	maxPositionsVal = 1_000_000
)

func invalidArg(msg string) error {
	return connect.NewError(connect.CodeInvalidArgument, errors.New(msg))
}

func checkName(name string) error {
	if name == "" {
		return invalidArg("name is required")
	}
	if len([]rune(name)) > maxNameLen {
		return invalidArg("name too long")
	}
	return nil
}

func checkLongText(s string) error {
	if len([]rune(s)) > maxLongTextLen {
		return invalidArg("text too long")
	}
	return nil
}

func checkDuration(d float64) error {
	if math.IsNaN(d) || math.IsInf(d, 0) || d < 0 || d > maxDurationS {
		return invalidArg("duration_target_s out of bounds")
	}
	return nil
}

func checkPosition(p *int32) error {
	if p != nil && (*p < 0 || *p > maxPositionsVal) {
		return invalidArg("position out of bounds")
	}
	return nil
}

// assetOfKind loads an asset and enforces its kind ("" = any). NotFound maps
// to InvalidArgument (a bad reference in the request), everything else stays
// a server error.
func (s *StoryServer) assetOfKind(ctx context.Context, ref *irisv1.AssetRef, kind, what string) error {
	if ref == nil || ref.AssetId == "" {
		return invalidArg(what + " asset is required")
	}
	a, _, err := s.Store.GetAsset(ctx, ref.AssetId)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return invalidArg(what + " asset not found")
		}
		return connectErr(err)
	}
	if kind != "" && a.Kind != kind {
		return invalidArg(what + " must be " + kind)
	}
	if ref.VersionId != "" {
		ok, err := s.Store.VersionBelongsToAsset(ctx, ref.VersionId, ref.AssetId)
		if err != nil {
			return connectErr(err)
		}
		if !ok {
			return invalidArg(what + " version does not belong to the asset")
		}
	}
	return nil
}

func (s *StoryServer) CreateScene(ctx context.Context, req *connect.Request[irisv1.CreateSceneRequest]) (*connect.Response[irisv1.CreateSceneResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, invalidArg("project_id is required")
	}
	if err := checkName(req.Msg.Name); err != nil {
		return nil, err
	}
	sc, err := s.Store.CreateScene(ctx, req.Msg.ProjectId, req.Msg.Name)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.CreateSceneResponse{Scene: scenePB(sc, nil, nil)}), nil
}

func (s *StoryServer) ListScenes(ctx context.Context, req *connect.Request[irisv1.ListScenesRequest]) (*connect.Response[irisv1.ListScenesResponse], error) {
	scenes, err := s.Store.ListScenes(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, connectErr(err)
	}
	resp := &irisv1.ListScenesResponse{}
	for _, sc := range scenes {
		resp.Scenes = append(resp.Scenes, scenePB(sc, nil, nil))
	}
	return connect.NewResponse(resp), nil
}

func (s *StoryServer) GetScene(ctx context.Context, req *connect.Request[irisv1.GetSceneRequest]) (*connect.Response[irisv1.GetSceneResponse], error) {
	sc, views, shots, err := s.Store.GetScene(ctx, req.Msg.Id)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.GetSceneResponse{Scene: scenePB(sc, views, shots)}), nil
}

func (s *StoryServer) UpdateScene(ctx context.Context, req *connect.Request[irisv1.UpdateSceneRequest]) (*connect.Response[irisv1.UpdateSceneResponse], error) {
	if req.Msg.Name != nil {
		if err := checkName(*req.Msg.Name); err != nil {
			return nil, err
		}
	}
	if req.Msg.StyleNotes != nil {
		if err := checkLongText(*req.Msg.StyleNotes); err != nil {
			return nil, err
		}
	}
	if err := checkPosition(req.Msg.Position); err != nil {
		return nil, err
	}
	sc, err := s.Store.UpdateScene(ctx, req.Msg.Id, req.Msg.Name, req.Msg.StyleNotes, req.Msg.Position)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.UpdateSceneResponse{Scene: scenePB(sc, nil, nil)}), nil
}

func (s *StoryServer) AddView(ctx context.Context, req *connect.Request[irisv1.AddViewRequest]) (*connect.Response[irisv1.AddViewResponse], error) {
	if req.Msg.SceneId == "" {
		return nil, invalidArg("scene_id is required")
	}
	if err := checkName(req.Msg.Name); err != nil {
		return nil, err
	}
	// The plate must be an existing image asset — a view is a reference
	// plate, not an arbitrary file.
	if err := s.assetOfKind(ctx, req.Msg.Plate, "image", "plate"); err != nil {
		return nil, err
	}
	v, err := s.Store.AddView(ctx, req.Msg.SceneId, req.Msg.Name, store.AssetRefJSON{
		AssetID: req.Msg.Plate.AssetId, VersionID: req.Msg.Plate.VersionId,
	})
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.AddViewResponse{View: viewPB(v)}), nil
}

func (s *StoryServer) RemoveView(ctx context.Context, req *connect.Request[irisv1.RemoveViewRequest]) (*connect.Response[irisv1.RemoveViewResponse], error) {
	if err := s.Store.RemoveView(ctx, req.Msg.Id); err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.RemoveViewResponse{}), nil
}

func (s *StoryServer) CreateShot(ctx context.Context, req *connect.Request[irisv1.CreateShotRequest]) (*connect.Response[irisv1.CreateShotResponse], error) {
	if req.Msg.SceneId == "" {
		return nil, invalidArg("scene_id is required")
	}
	if err := checkLongText(req.Msg.Description); err != nil {
		return nil, err
	}
	if err := checkDuration(req.Msg.DurationTargetS); err != nil {
		return nil, err
	}
	sh, err := s.Store.CreateShot(ctx, req.Msg.SceneId, req.Msg.Description, req.Msg.DurationTargetS)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.CreateShotResponse{Shot: shotPB(sh)}), nil
}

func (s *StoryServer) UpdateShot(ctx context.Context, req *connect.Request[irisv1.UpdateShotRequest]) (*connect.Response[irisv1.UpdateShotResponse], error) {
	m := req.Msg
	if m.Description != nil {
		if err := checkLongText(*m.Description); err != nil {
			return nil, err
		}
	}
	if m.DurationTargetS != nil {
		if err := checkDuration(*m.DurationTargetS); err != nil {
			return nil, err
		}
	}
	if err := checkPosition(m.Position); err != nil {
		return nil, err
	}
	if m.SetCast {
		if len(m.CastIds) > maxCastSize {
			return nil, invalidArg("cast too large")
		}
		ok, err := s.Store.CharactersExist(ctx, store.DevWorkspaceID, m.CastIds)
		if err != nil {
			return nil, connectErr(err)
		}
		if !ok {
			return nil, invalidArg("cast contains unknown characters")
		}
	}
	sh, err := s.Store.UpdateShot(ctx, m.Id, m.Description, m.DurationTargetS, m.ViewId, m.CastIds, m.SetCast, m.Position, m.Pinned, m.ClearView)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.UpdateShotResponse{Shot: shotPB(sh)}), nil
}

func (s *StoryServer) DeleteShot(ctx context.Context, req *connect.Request[irisv1.DeleteShotRequest]) (*connect.Response[irisv1.DeleteShotResponse], error) {
	if err := s.Store.DeleteShot(ctx, req.Msg.Id); err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.DeleteShotResponse{}), nil
}

func (s *StoryServer) ListTakes(ctx context.Context, req *connect.Request[irisv1.ListTakesRequest]) (*connect.Response[irisv1.ListTakesResponse], error) {
	takes, err := s.Store.ListTakes(ctx, req.Msg.ShotId)
	if err != nil {
		return nil, connectErr(err)
	}
	resp := &irisv1.ListTakesResponse{}
	for _, t := range takes {
		resp.Takes = append(resp.Takes, takePB(t))
	}
	return connect.NewResponse(resp), nil
}

func (s *StoryServer) GetSceneChains(ctx context.Context, req *connect.Request[irisv1.GetSceneChainsRequest]) (*connect.Response[irisv1.GetSceneChainsResponse], error) {
	if req.Msg.SceneId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("scene_id is required"))
	}
	edges, err := s.Store.GetSceneChains(ctx, req.Msg.SceneId)
	if err != nil {
		return nil, connectErr(err)
	}
	resp := &irisv1.GetSceneChainsResponse{}
	for _, e := range edges {
		resp.Edges = append(resp.Edges, &irisv1.ChainEdge{
			FromShotId: e.FromShotID, ToShotId: e.ToShotID,
			CarriedVersionId: e.CarriedVersionID, Fresh: e.Fresh,
		})
	}
	return connect.NewResponse(resp), nil
}

func (s *StoryServer) GetShot(ctx context.Context, req *connect.Request[irisv1.GetShotRequest]) (*connect.Response[irisv1.GetShotResponse], error) {
	sh, err := s.Store.GetShot(ctx, req.Msg.Id)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.GetShotResponse{Shot: shotPB(sh)}), nil
}

func (s *StoryServer) SelectTake(ctx context.Context, req *connect.Request[irisv1.SelectTakeRequest]) (*connect.Response[irisv1.SelectTakeResponse], error) {
	if err := s.Store.SelectTake(ctx, req.Msg.ShotId, req.Msg.TakeId); err != nil {
		return nil, connectErr(err)
	}
	sh, err := s.Store.GetShot(ctx, req.Msg.ShotId)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.SelectTakeResponse{Shot: shotPB(sh)}), nil
}

func (s *StoryServer) CreateCharacter(ctx context.Context, req *connect.Request[irisv1.CreateCharacterRequest]) (*connect.Response[irisv1.CreateCharacterResponse], error) {
	if err := checkName(req.Msg.Name); err != nil {
		return nil, err
	}
	c, err := s.Store.CreateCharacter(ctx, workspaceID(req.Msg.WorkspaceId), req.Msg.ProjectId, req.Msg.Name)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.CreateCharacterResponse{Character: characterPB(c)}), nil
}

func (s *StoryServer) ListCharacters(ctx context.Context, req *connect.Request[irisv1.ListCharactersRequest]) (*connect.Response[irisv1.ListCharactersResponse], error) {
	chars, err := s.Store.ListCharacters(ctx, workspaceID(req.Msg.WorkspaceId), req.Msg.ProjectId)
	if err != nil {
		return nil, connectErr(err)
	}
	resp := &irisv1.ListCharactersResponse{}
	for _, c := range chars {
		resp.Characters = append(resp.Characters, characterPB(c))
	}
	return connect.NewResponse(resp), nil
}

func (s *StoryServer) UpdateCharacter(ctx context.Context, req *connect.Request[irisv1.UpdateCharacterRequest]) (*connect.Response[irisv1.UpdateCharacterResponse], error) {
	if req.Msg.Name != nil {
		if err := checkName(*req.Msg.Name); err != nil {
			return nil, err
		}
	}
	c, err := s.Store.UpdateCharacter(ctx, req.Msg.Id, req.Msg.Name, req.Msg.VoiceId)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.UpdateCharacterResponse{Character: characterPB(c)}), nil
}

// Character ref roles imply asset kinds: visual refs must be images, voice
// refs audio; "other" is unconstrained.
var refRoleKinds = map[string]string{
	"turnaround": "image",
	"expression": "image",
	"voice":      "audio",
	"other":      "",
}

func (s *StoryServer) AddCharacterRef(ctx context.Context, req *connect.Request[irisv1.AddCharacterRefRequest]) (*connect.Response[irisv1.AddCharacterRefResponse], error) {
	m := req.Msg
	if m.CharacterId == "" || m.Role == "" {
		return nil, invalidArg("character_id and role are required")
	}
	kind, ok := refRoleKinds[m.Role]
	if !ok {
		return nil, invalidArg("unknown character ref role")
	}
	if err := s.assetOfKind(ctx, m.Asset, kind, "ref"); err != nil {
		return nil, err
	}
	c, err := s.Store.AddCharacterRef(ctx, m.CharacterId, store.CharacterRefJSON{
		Role:  m.Role,
		Asset: store.AssetRefJSON{AssetID: m.Asset.AssetId, VersionID: m.Asset.VersionId},
	})
	if err != nil {
		if errors.Is(err, store.ErrRefLimit) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		}
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.AddCharacterRefResponse{Character: characterPB(c)}), nil
}

func (s *StoryServer) RemoveCharacterRef(ctx context.Context, req *connect.Request[irisv1.RemoveCharacterRefRequest]) (*connect.Response[irisv1.RemoveCharacterRefResponse], error) {
	m := req.Msg
	if m.CharacterId == "" || m.Role == "" || m.AssetId == "" {
		return nil, invalidArg("character_id, role, and asset_id are required")
	}
	c, err := s.Store.RemoveCharacterRef(ctx, m.CharacterId, m.Role, m.AssetId)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.RemoveCharacterRefResponse{Character: characterPB(c)}), nil
}

// ── mapping ───────────────────────────────────────────────────────────────────

func scenePB(sc *store.Scene, views []*store.ViewRow, shots []*store.ShotRow) *irisv1.Scene {
	pb := &irisv1.Scene{
		Id: sc.ID, ProjectId: sc.ProjectID, Name: sc.Name,
		Position: sc.Position, StyleNotes: sc.StyleNotes,
		Timestamps: ts(sc.CreatedAt, sc.UpdatedAt),
	}
	if sc.Model3D != nil {
		pb.Model3D = &irisv1.AssetRef{AssetId: sc.Model3D.AssetID, VersionId: sc.Model3D.VersionID}
	}
	for _, v := range views {
		pb.Views = append(pb.Views, viewPB(v))
	}
	for _, sh := range shots {
		pb.Shots = append(pb.Shots, shotPB(sh))
	}
	return pb
}

func viewPB(v *store.ViewRow) *irisv1.View {
	return &irisv1.View{
		Id: v.ID, SceneId: v.SceneID, Name: v.Name, Position: v.Position,
		Plate: &irisv1.AssetRef{AssetId: v.Plate.AssetID, VersionId: v.Plate.VersionID},
	}
}

func shotPB(sh *store.ShotRow) *irisv1.Shot {
	return &irisv1.Shot{
		Id: sh.ID, SceneId: sh.SceneID, Position: sh.Position,
		Description: sh.Description, DurationTargetS: sh.DurationTargetS,
		ViewId: sh.ViewID, CastIds: sh.CastIDs, SelectedTakeId: sh.SelectedTakeID,
		SelectedTakeVersionId:   sh.SelectedTakeVersionID,
		SelectedTakeContentType: sh.SelectedTakeContentType,
		ContinuityStale:         sh.ContinuityStale, Pinned: sh.Pinned, TakeCount: sh.TakeCount,
		Timestamps: ts(sh.CreatedAt, sh.UpdatedAt),
	}
}

func takePB(t *store.TakeRow) *irisv1.Take {
	pb := &irisv1.Take{
		Id: t.ID, ShotId: t.ShotID, JobId: t.JobID, VersionId: t.VersionID,
		Quality: t.Quality, Starred: t.Starred, RecipeJson: string(t.Recipe),
		Timestamps: ts(t.CreatedAt, t.CreatedAt),
	}
	// Takes are immutable (bar starring) — don't fabricate an updated_at.
	pb.Timestamps.UpdatedAt = nil
	return pb
}

func characterPB(c *store.CharacterRow) *irisv1.Character {
	pb := &irisv1.Character{
		Id: c.ID, WorkspaceId: c.WorkspaceID, ProjectId: c.ProjectID, Name: c.Name,
		VoiceId:    c.VoiceID,
		Timestamps: ts(c.CreatedAt, c.UpdatedAt),
	}
	for _, r := range c.Refs {
		pb.Refs = append(pb.Refs, &irisv1.CharacterRef{
			Role:  r.Role,
			Asset: &irisv1.AssetRef{AssetId: r.Asset.AssetID, VersionId: r.Asset.VersionID},
		})
	}
	return pb
}
