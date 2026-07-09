package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	irisv1 "github.com/awm33/iris/backend/gen/iris/v1"
	"github.com/awm33/iris/backend/internal/store"
)

// TimelineServer: same posture as CanvasServer — server orders and persists,
// op payload semantics stay client-owned (envelope + size validation only).
type TimelineServer struct {
	Store *store.Store
}

func (s *TimelineServer) CreateTimeline(ctx context.Context, req *connect.Request[irisv1.CreateTimelineRequest]) (*connect.Response[irisv1.CreateTimelineResponse], error) {
	m := req.Msg
	name := strings.TrimSpace(m.Name)
	if m.ProjectId == "" || name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("project_id and name are required"))
	}
	fps := m.Fps
	if fps == 0 {
		fps = 24
	}
	if fps < 1 || fps > 240 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("fps must be 1..240"))
	}
	t := &store.Timeline{
		WorkspaceID: store.DevWorkspaceID,
		ProjectID:   m.ProjectId,
		Name:        truncateRunes(name, 200),
		FPS:         fps,
	}
	if err := s.Store.CreateTimeline(ctx, t); err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.CreateTimelineResponse{Timeline: timelinePB(t)}), nil
}

func (s *TimelineServer) ListTimelines(ctx context.Context, req *connect.Request[irisv1.ListTimelinesRequest]) (*connect.Response[irisv1.ListTimelinesResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("project_id is required"))
	}
	ts, err := s.Store.ListTimelines(ctx, store.DevWorkspaceID, req.Msg.ProjectId)
	if err != nil {
		return nil, connectErr(err)
	}
	resp := &irisv1.ListTimelinesResponse{}
	for _, t := range ts {
		resp.Timelines = append(resp.Timelines, timelinePB(t))
	}
	return connect.NewResponse(resp), nil
}

func (s *TimelineServer) GetTimeline(ctx context.Context, req *connect.Request[irisv1.GetTimelineRequest]) (*connect.Response[irisv1.GetTimelineResponse], error) {
	if req.Msg.AfterSeq < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("after_seq must be >= 0"))
	}
	t, err := s.Store.GetTimeline(ctx, req.Msg.Id)
	if err != nil {
		return nil, connectErr(err)
	}
	ops, err := s.Store.ListCanvasOps(ctx, t.DocID, req.Msg.AfterSeq, maxOpsPerGet)
	if err != nil {
		return nil, connectErr(err)
	}
	resp := &irisv1.GetTimelineResponse{Timeline: timelinePB(t)}
	for _, op := range ops {
		resp.Ops = append(resp.Ops, &irisv1.DocOp{Seq: op.Seq, ActorId: op.ActorID, Payload: string(op.Payload)})
	}
	return connect.NewResponse(resp), nil
}

func (s *TimelineServer) AppendTimelineOps(ctx context.Context, req *connect.Request[irisv1.AppendTimelineOpsRequest]) (*connect.Response[irisv1.AppendTimelineOpsResponse], error) {
	m := req.Msg
	if m.TimelineId == "" || m.BaseSeq < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("timeline_id and base_seq >= 0 are required"))
	}
	if len(m.Payloads) == 0 || len(m.Payloads) > maxOpsPerAppend {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("1..%d ops per append", maxOpsPerAppend))
	}
	payloads := make([][]byte, len(m.Payloads))
	for i, p := range m.Payloads {
		if len(p) > maxOpBytes {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("op %d exceeds %dKB", i, maxOpBytes>>10))
		}
		if err := validateOpPayload(p); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("op %d: %w", i, err))
		}
		payloads[i] = []byte(p)
	}
	head, err := s.Store.AppendTimelineOps(ctx, m.TimelineId, m.BaseSeq, devActor, payloads)
	if errors.Is(err, store.ErrSeqConflict) {
		return nil, connect.NewError(connect.CodeAborted,
			fmt.Errorf("base_seq %d is stale (head is %d) — refetch and replay", m.BaseSeq, head))
	}
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.AppendTimelineOpsResponse{HeadSeq: head}), nil
}

func (s *TimelineServer) DeleteTimeline(ctx context.Context, req *connect.Request[irisv1.DeleteTimelineRequest]) (*connect.Response[irisv1.DeleteTimelineResponse], error) {
	if err := s.Store.DeleteTimeline(ctx, req.Msg.Id); err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.DeleteTimelineResponse{}), nil
}

func timelinePB(t *store.Timeline) *irisv1.Timeline {
	return &irisv1.Timeline{
		Id: t.ID, ProjectId: t.ProjectID, DocId: t.DocID, Name: t.Name,
		Fps: t.FPS, HeadSeq: t.HeadSeq, Timestamps: ts(t.CreatedAt, t.UpdatedAt),
	}
}
