package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"

	irisv1 "github.com/awm33/iris/backend/gen/iris/v1"
	"github.com/awm33/iris/backend/internal/blob"
	"github.com/awm33/iris/backend/internal/store"
)

// CanvasServer orders and persists canvas op logs. Op payloads are a
// client-owned vocabulary (web/packages/doc-runtime); the server validates
// shape and size, assigns seqs, and stays out of semantics — the same
// untrusted-input posture as everywhere else, applied to our own client.
// It also proxies the SAM subject-select tool service (SubjectMask): the
// browser never talks to model containers directly.
type CanvasServer struct {
	Store  *store.Store
	Blob   *blob.Store
	SamURL string // "" = subject select not configured
	HTTP   *http.Client
}

const (
	canvasMinDim    = 16
	canvasMaxDim    = 16384
	maxOpsPerAppend = 200
	maxOpBytes      = 64 << 10
	maxOpsPerGet    = 2000
	devActor        = "dev" // auth v0: single-user
)

func (s *CanvasServer) CreateCanvas(ctx context.Context, req *connect.Request[irisv1.CreateCanvasRequest]) (*connect.Response[irisv1.CreateCanvasResponse], error) {
	m := req.Msg
	name := strings.TrimSpace(m.Name)
	if m.ProjectId == "" || name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("project_id and name are required"))
	}
	if m.Width < canvasMinDim || m.Width > canvasMaxDim || m.Height < canvasMinDim || m.Height > canvasMaxDim {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("width/height must be %d..%d", canvasMinDim, canvasMaxDim))
	}
	c := &store.Canvas{
		WorkspaceID: store.DevWorkspaceID,
		ProjectID:   m.ProjectId,
		Name:        truncateRunes(name, 200),
		Width:       m.Width,
		Height:      m.Height,
	}
	if err := s.Store.CreateCanvas(ctx, c); err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.CreateCanvasResponse{Canvas: canvasPB(c)}), nil
}

func (s *CanvasServer) ListCanvases(ctx context.Context, req *connect.Request[irisv1.ListCanvasesRequest]) (*connect.Response[irisv1.ListCanvasesResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("project_id is required"))
	}
	canvases, err := s.Store.ListCanvases(ctx, store.DevWorkspaceID, req.Msg.ProjectId)
	if err != nil {
		return nil, connectErr(err)
	}
	resp := &irisv1.ListCanvasesResponse{}
	for _, c := range canvases {
		resp.Canvases = append(resp.Canvases, canvasPB(c))
	}
	return connect.NewResponse(resp), nil
}

func (s *CanvasServer) GetCanvas(ctx context.Context, req *connect.Request[irisv1.GetCanvasRequest]) (*connect.Response[irisv1.GetCanvasResponse], error) {
	if req.Msg.AfterSeq < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("after_seq must be >= 0"))
	}
	c, err := s.Store.GetCanvas(ctx, req.Msg.Id)
	if err != nil {
		return nil, connectErr(err)
	}
	ops, err := s.Store.ListCanvasOps(ctx, c.DocID, req.Msg.AfterSeq, maxOpsPerGet)
	if err != nil {
		return nil, connectErr(err)
	}
	resp := &irisv1.GetCanvasResponse{Canvas: canvasPB(c)}
	for _, op := range ops {
		resp.Ops = append(resp.Ops, &irisv1.DocOp{
			Seq: op.Seq, ActorId: op.ActorID, Payload: string(op.Payload),
		})
	}
	return connect.NewResponse(resp), nil
}

func (s *CanvasServer) AppendOps(ctx context.Context, req *connect.Request[irisv1.AppendOpsRequest]) (*connect.Response[irisv1.AppendOpsResponse], error) {
	m := req.Msg
	if m.CanvasId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("canvas_id is required"))
	}
	if m.BaseSeq < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("base_seq must be >= 0"))
	}
	if len(m.Payloads) == 0 || len(m.Payloads) > maxOpsPerAppend {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("1..%d ops per append", maxOpsPerAppend))
	}
	payloads := make([][]byte, len(m.Payloads))
	for i, p := range m.Payloads {
		if len(p) > maxOpBytes {
			return nil, connect.NewError(connect.CodeInvalidArgument,
				fmt.Errorf("op %d exceeds %dKB", i, maxOpBytes>>10))
		}
		if err := validateOpPayload(p); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("op %d: %w", i, err))
		}
		payloads[i] = []byte(p)
	}
	head, err := s.Store.AppendCanvasOps(ctx, m.CanvasId, m.BaseSeq, devActor, payloads)
	if errors.Is(err, store.ErrSeqConflict) {
		// head carries the current head_seq — the client refetches from its
		// last applied seq and replays before retrying.
		return nil, connect.NewError(connect.CodeAborted,
			fmt.Errorf("base_seq %d is stale (head is %d) — refetch and replay", m.BaseSeq, head))
	}
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.AppendOpsResponse{HeadSeq: head}), nil
}

// validateOpPayload enforces the envelope only: a JSON object carrying
// non-empty op_id and type strings. Semantics stay client-owned.
// json.Unmarshal (not Decoder.Decode) so trailing garbage is rejected here
// with a client error instead of failing the whole batch at the jsonb insert.
func validateOpPayload(p string) error {
	var env struct {
		OpID string `json:"op_id"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(p), &env); err != nil {
		return fmt.Errorf("payload is not valid JSON: %w", err)
	}
	if env.OpID == "" || env.Type == "" {
		return errors.New("payload must carry op_id and type")
	}
	return nil
}

func (s *CanvasServer) UpdateCanvas(ctx context.Context, req *connect.Request[irisv1.UpdateCanvasRequest]) (*connect.Response[irisv1.UpdateCanvasResponse], error) {
	name := strings.TrimSpace(req.Msg.Name)
	if req.Msg.Id == "" || name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("id and name are required"))
	}
	c, err := s.Store.RenameCanvas(ctx, req.Msg.Id, truncateRunes(name, 200))
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.UpdateCanvasResponse{Canvas: canvasPB(c)}), nil
}

func (s *CanvasServer) DeleteCanvas(ctx context.Context, req *connect.Request[irisv1.DeleteCanvasRequest]) (*connect.Response[irisv1.DeleteCanvasResponse], error) {
	if err := s.Store.DeleteCanvas(ctx, req.Msg.Id); err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.DeleteCanvasResponse{}), nil
}

const maxSubjectPoints = 16

func (s *CanvasServer) SubjectMask(ctx context.Context, req *connect.Request[irisv1.SubjectMaskRequest]) (*connect.Response[irisv1.SubjectMaskResponse], error) {
	if s.SamURL == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("subject select not configured — start the sam service and set IRIS_SAM_URL"))
	}
	m := req.Msg
	if m.VersionId == "" || len(m.Points) == 0 || len(m.Points) > maxSubjectPoints {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("version_id and 1..%d points are required", maxSubjectPoints))
	}
	info, err := s.Store.GetVersionObjectInfo(ctx, m.VersionId)
	if err != nil {
		return nil, connectErr(err)
	}
	if !strings.HasPrefix(info.ContentType, "image/") {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("subject select needs an image version"))
	}
	// External presign: the tool container fetches via host.docker.internal,
	// same as generation endpoints.
	url, err := s.Blob.PresignGetExternal(ctx, blob.ContentKey(info.SHA256), info.ContentType, 5*time.Minute)
	if err != nil {
		return nil, connectErr(err)
	}
	points := make([][3]float64, 0, len(m.Points))
	for _, p := range m.Points {
		label := 1.0
		if p.Negative {
			label = 0
		}
		points = append(points, [3]float64{p.X, p.Y, label})
	}
	body, _ := json.Marshal(map[string]any{"image_url": url, "points": points})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.SamURL+"/mask", bytes.NewReader(body))
	if err != nil {
		return nil, connectErr(err)
	}
	httpReq.Header.Set("Authorization", "Bearer dev")
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := s.HTTP.Do(httpReq)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("subject-select service: %w", err))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	if resp.StatusCode != 200 {
		return nil, connect.NewError(connect.CodeUnavailable,
			fmt.Errorf("subject-select service: HTTP %d: %s", resp.StatusCode, truncateRunes(string(data), 200)))
	}
	return connect.NewResponse(&irisv1.SubjectMaskResponse{MaskPng: data}), nil
}

func canvasPB(c *store.Canvas) *irisv1.Canvas {
	return &irisv1.Canvas{
		Id: c.ID, ProjectId: c.ProjectID, DocId: c.DocID, Name: c.Name,
		Width: c.Width, Height: c.Height, HeadSeq: c.HeadSeq,
		Timestamps: ts(c.CreatedAt, c.UpdatedAt),
	}
}
