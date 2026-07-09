package api

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	irisv1 "github.com/awm33/iris/backend/gen/iris/v1"
	"github.com/awm33/iris/backend/internal/store"
)

type StoryServer struct {
	Store *store.Store
}

func (s *StoryServer) CreateScene(ctx context.Context, req *connect.Request[irisv1.CreateSceneRequest]) (*connect.Response[irisv1.CreateSceneResponse], error) {
	if req.Msg.ProjectId == "" || req.Msg.Name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("project_id and name are required"))
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
	sc, err := s.Store.UpdateScene(ctx, req.Msg.Id, req.Msg.Name, req.Msg.StyleNotes, req.Msg.Position)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.UpdateSceneResponse{Scene: scenePB(sc, nil, nil)}), nil
}

func (s *StoryServer) AddView(ctx context.Context, req *connect.Request[irisv1.AddViewRequest]) (*connect.Response[irisv1.AddViewResponse], error) {
	if req.Msg.SceneId == "" || req.Msg.Name == "" || req.Msg.Plate == nil || req.Msg.Plate.AssetId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("scene_id, name, and plate.asset_id are required"))
	}
	// The plate must be an existing image asset — a view is a reference
	// plate, not an arbitrary file.
	a, _, err := s.Store.GetAsset(ctx, req.Msg.Plate.AssetId)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("plate asset not found"))
	}
	if a.Kind != "image" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("view plates must be images"))
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
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("scene_id is required"))
	}
	sh, err := s.Store.CreateShot(ctx, req.Msg.SceneId, req.Msg.Description, req.Msg.DurationTargetS)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.CreateShotResponse{Shot: shotPB(sh)}), nil
}

func (s *StoryServer) UpdateShot(ctx context.Context, req *connect.Request[irisv1.UpdateShotRequest]) (*connect.Response[irisv1.UpdateShotResponse], error) {
	m := req.Msg
	sh, err := s.Store.UpdateShot(ctx, m.Id, m.Description, m.DurationTargetS, m.ViewId, m.CastIds, m.SetCast, m.Position, m.Pinned)
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
	if req.Msg.Name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name is required"))
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
	c, err := s.Store.UpdateCharacter(ctx, req.Msg.Id, req.Msg.Name)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.UpdateCharacterResponse{Character: characterPB(c)}), nil
}

func (s *StoryServer) AddCharacterRef(ctx context.Context, req *connect.Request[irisv1.AddCharacterRefRequest]) (*connect.Response[irisv1.AddCharacterRefResponse], error) {
	m := req.Msg
	if m.CharacterId == "" || m.Role == "" || m.Asset == nil || m.Asset.AssetId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("character_id, role, and asset.asset_id are required"))
	}
	switch m.Role {
	case "turnaround", "expression", "voice", "other":
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("unknown character ref role"))
	}
	if _, _, err := s.Store.GetAsset(ctx, m.Asset.AssetId); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("ref asset not found"))
	}
	c, err := s.Store.AddCharacterRef(ctx, m.CharacterId, store.CharacterRefJSON{
		Role:  m.Role,
		Asset: store.AssetRefJSON{AssetID: m.Asset.AssetId, VersionID: m.Asset.VersionId},
	})
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.AddCharacterRefResponse{Character: characterPB(c)}), nil
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
		ContinuityStale: sh.ContinuityStale, Pinned: sh.Pinned, TakeCount: sh.TakeCount,
		Timestamps: ts(sh.CreatedAt, sh.UpdatedAt),
	}
}

func takePB(t *store.TakeRow) *irisv1.Take {
	return &irisv1.Take{
		Id: t.ID, ShotId: t.ShotID, JobId: t.JobID, VersionId: t.VersionID,
		Quality: t.Quality, Starred: t.Starred, RecipeJson: string(t.Recipe),
		Timestamps: ts(t.CreatedAt, t.CreatedAt),
	}
}

func characterPB(c *store.CharacterRow) *irisv1.Character {
	pb := &irisv1.Character{
		Id: c.ID, WorkspaceId: c.WorkspaceID, ProjectId: c.ProjectID, Name: c.Name,
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
