package api

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/protobuf/types/known/timestamppb"

	irisv1 "github.com/awm33/iris/backend/gen/iris/v1"
	"github.com/awm33/iris/backend/internal/store"
)

type WorkspaceServer struct {
	Store *store.Store
}

func (s *WorkspaceServer) GetWorkspace(ctx context.Context, req *connect.Request[irisv1.GetWorkspaceRequest]) (*connect.Response[irisv1.GetWorkspaceResponse], error) {
	w, err := s.Store.GetWorkspace(ctx, req.Msg.Id)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.GetWorkspaceResponse{Workspace: &irisv1.Workspace{
		Id: w.ID, Name: w.Name, Timestamps: ts(w.CreatedAt, w.UpdatedAt),
	}}), nil
}

func (s *WorkspaceServer) CreateProject(ctx context.Context, req *connect.Request[irisv1.CreateProjectRequest]) (*connect.Response[irisv1.CreateProjectResponse], error) {
	if req.Msg.Name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name is required"))
	}
	p, err := s.Store.CreateProject(ctx, workspaceID(req.Msg.WorkspaceId), req.Msg.Name, req.Msg.Description)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.CreateProjectResponse{Project: projectPB(p)}), nil
}

func (s *WorkspaceServer) ListProjects(ctx context.Context, req *connect.Request[irisv1.ListProjectsRequest]) (*connect.Response[irisv1.ListProjectsResponse], error) {
	projects, err := s.Store.ListProjects(ctx, workspaceID(req.Msg.WorkspaceId))
	if err != nil {
		return nil, connectErr(err)
	}
	resp := &irisv1.ListProjectsResponse{}
	for _, p := range projects {
		resp.Projects = append(resp.Projects, projectPB(p))
	}
	return connect.NewResponse(resp), nil
}

func (s *WorkspaceServer) GetProject(ctx context.Context, req *connect.Request[irisv1.GetProjectRequest]) (*connect.Response[irisv1.GetProjectResponse], error) {
	p, err := s.Store.GetProject(ctx, req.Msg.Id)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.GetProjectResponse{Project: projectPB(p)}), nil
}

func (s *WorkspaceServer) UpdateProject(ctx context.Context, req *connect.Request[irisv1.UpdateProjectRequest]) (*connect.Response[irisv1.UpdateProjectResponse], error) {
	p, err := s.Store.UpdateProject(ctx, req.Msg.Id, req.Msg.Name, req.Msg.Description)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.UpdateProjectResponse{Project: projectPB(p)}), nil
}

func (s *WorkspaceServer) ArchiveProject(ctx context.Context, req *connect.Request[irisv1.ArchiveProjectRequest]) (*connect.Response[irisv1.ArchiveProjectResponse], error) {
	if err := s.Store.ArchiveProject(ctx, req.Msg.Id); err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.ArchiveProjectResponse{}), nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

// workspaceID resolves the effective workspace (auth v0: everything is the
// seeded dev workspace unless the request names one).
func workspaceID(requested string) string {
	if requested != "" {
		return requested
	}
	return store.DevWorkspaceID
}

func projectPB(p *store.Project) *irisv1.Project {
	return &irisv1.Project{
		Id: p.ID, WorkspaceId: p.WorkspaceID, Name: p.Name, Description: p.Description,
		Timestamps: ts(p.CreatedAt, p.UpdatedAt),
	}
}

func ts(created, updated store.Time) *irisv1.Timestamps {
	return &irisv1.Timestamps{
		CreatedAt: timestamppb.New(created),
		UpdatedAt: timestamppb.New(updated),
	}
}

func connectErr(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return connect.NewError(connect.CodeNotFound, err)
	}
	// FK/unique violations are client errors (bad references), not server
	// faults — and raw Postgres text should not leak to clients.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23503":
			return connect.NewError(connect.CodeInvalidArgument, errors.New("referenced entity does not exist"))
		case "23505":
			return connect.NewError(connect.CodeAlreadyExists, errors.New("already exists"))
		}
	}
	return connect.NewError(connect.CodeInternal, err)
}
