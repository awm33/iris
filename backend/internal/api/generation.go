package api

import (
	"context"
	"encoding/json"
	"errors"

	"connectrpc.com/connect"

	irisv1 "github.com/awm33/iris/backend/gen/iris/v1"
	"github.com/awm33/iris/backend/internal/inference"
	"github.com/awm33/iris/backend/internal/registry"
	"github.com/awm33/iris/backend/internal/store"
)

type GenerationServer struct {
	Store    *store.Store
	Registry *registry.Registry
}

const maxFanout = 8

func (s *GenerationServer) CreateJob(ctx context.Context, req *connect.Request[irisv1.CreateJobRequest]) (*connect.Response[irisv1.CreateJobResponse], error) {
	j := req.Msg.Job
	if j == nil || j.Prompt == "" || j.ModelEndpointId == "" || j.Task == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("job.prompt, job.model_endpoint_id, and job.task are required"))
	}
	if j.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("job.project_id is required"))
	}
	count := int(j.Count)
	if count < 1 {
		count = 1
	}
	if count > maxFanout {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("count exceeds fan-out limit"))
	}
	if j.Profile == "" {
		j.Profile = "draft"
	}

	ep, ok := s.Registry.Get(j.ModelEndpointId)
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("unknown model endpoint"))
	}
	if !ep.Healthy || ep.Manifest == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("endpoint unhealthy or manifest unavailable"))
	}

	genReq, infReq, err := s.toGenRequest(j, count)
	if err != nil {
		return nil, err
	}
	// Validate NOW so the user gets capability errors at click time, not from
	// a failed job minutes later. (The orchestrator re-validates at dispatch.)
	if err := ep.Manifest.Validate(infReq); err != nil {
		var verr *inference.ValidationError
		if errors.As(err, &verr) {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		return nil, connectErr(err)
	}

	reqJSON, _ := json.Marshal(genReq)
	parent := &store.GenJob{
		WorkspaceID:    workspaceID(j.WorkspaceId),
		ProjectID:      j.ProjectId,
		EndpointID:     j.ModelEndpointId,
		DependsOnJobID: j.DependsOnJobId,
		Task:           j.Task,
		Profile:        j.Profile,
		Request:        reqJSON,
		TargetEntityID: j.TargetEntityId,
		CostEstimate:   ep.Manifest.Pricing.Estimates[j.Profile] * float64(count),
	}
	if _, err := s.Store.CreateGenerationFanout(ctx, parent, count); err != nil {
		return nil, connectErr(err)
	}
	created, err := s.Store.GetGenJob(ctx, parent.ID)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.CreateJobResponse{Job: genJobPB(created)}), nil
}

// toGenRequest converts the proto job into the stored request and its
// inference-request twin used for validation.
func (s *GenerationServer) toGenRequest(j *irisv1.GenerationJob, count int) (*store.GenRequest, *inference.CreateJobRequest, error) {
	genReq := &store.GenRequest{
		Prompt:         j.Prompt,
		NegativePrompt: j.NegativePrompt,
		Seed:           j.Seed,
		Count:          count,
	}
	if j.ParamsJson != "" {
		if !json.Valid([]byte(j.ParamsJson)) {
			return nil, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("params_json is not valid JSON"))
		}
		genReq.Params = json.RawMessage(j.ParamsJson)
	}

	infReq := &inference.CreateJobRequest{
		Task:           j.Task,
		Profile:        j.Profile,
		Prompt:         j.Prompt,
		NegativePrompt: j.NegativePrompt,
		Seed:           j.Seed,
		Params:         genReq.Params,
	}
	if j.Output != nil {
		out := inference.Output{
			Width:     int(j.Output.Width),
			Height:    int(j.Output.Height),
			DurationS: j.Output.DurationS,
			FPS:       j.Output.Fps,
		}
		infReq.Output = &out
		outJSON, _ := json.Marshal(out)
		genReq.Output = outJSON
	}
	for _, r := range j.References {
		if r.Asset == nil || r.Asset.AssetId == "" {
			return nil, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("reference missing asset"))
		}
		genReq.References = append(genReq.References, store.GenRef{
			Kind: r.Kind, Role: r.Role,
			AssetID: r.Asset.AssetId, VersionID: r.Asset.VersionId,
			Weight: r.Weight,
		})
		// URL filled at dispatch; validation only needs kind/role/counts.
		infReq.References = append(infReq.References, inference.Reference{
			Kind: r.Kind, Role: r.Role, Weight: r.Weight,
		})
	}
	// Conditioning payloads (frames/depth/mask) arrive with M3+ surfaces;
	// the proto carries them but M2 validates only refs/output/params.
	return genReq, infReq, nil
}

func (s *GenerationServer) GetJob(ctx context.Context, req *connect.Request[irisv1.GetJobRequest]) (*connect.Response[irisv1.GetJobResponse], error) {
	j, err := s.Store.GetGenJob(ctx, req.Msg.Id)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.GetJobResponse{Job: genJobPB(j)}), nil
}

func (s *GenerationServer) ListJobs(ctx context.Context, req *connect.Request[irisv1.ListJobsRequest]) (*connect.Response[irisv1.ListJobsResponse], error) {
	state := ""
	if req.Msg.State != irisv1.JobState_JOB_STATE_UNSPECIFIED {
		state = stateString(req.Msg.State)
	}
	jobs, err := s.Store.ListGenJobs(ctx, req.Msg.ProjectId, state)
	if err != nil {
		return nil, connectErr(err)
	}
	resp := &irisv1.ListJobsResponse{}
	for _, j := range jobs {
		resp.Jobs = append(resp.Jobs, genJobPB(j))
	}
	return connect.NewResponse(resp), nil
}

func (s *GenerationServer) CancelJob(ctx context.Context, req *connect.Request[irisv1.CancelJobRequest]) (*connect.Response[irisv1.CancelJobResponse], error) {
	// Flipping the rows is sufficient: the owning worker notices on its next
	// heartbeat (≤1 poll interval) and cancels the endpoint-side job using
	// the per-attempt id only it knows.
	if _, err := s.Store.CancelGeneration(ctx, req.Msg.Id); err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.CancelJobResponse{}), nil
}

func (s *GenerationServer) RetryJob(ctx context.Context, req *connect.Request[irisv1.RetryJobRequest]) (*connect.Response[irisv1.RetryJobResponse], error) {
	prev, err := s.Store.GetGenJob(ctx, req.Msg.Id)
	if err != nil {
		return nil, connectErr(err)
	}
	var genReq store.GenRequest
	_ = json.Unmarshal(prev.Request, &genReq)
	count := genReq.Count
	if count < 1 {
		count = 1
	}
	parent := &store.GenJob{
		WorkspaceID:    prev.WorkspaceID,
		ProjectID:      prev.ProjectID,
		EndpointID:     prev.EndpointID,
		DependsOnJobID: prev.DependsOnJobID,
		Task:           prev.Task,
		Profile:        prev.Profile,
		Request:        prev.Request,
		TargetEntityID: prev.TargetEntityID,
		CostEstimate:   prev.CostEstimate,
	}
	if _, err := s.Store.CreateGenerationFanout(ctx, parent, count); err != nil {
		return nil, connectErr(err)
	}
	created, err := s.Store.GetGenJob(ctx, parent.ID)
	if err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.RetryJobResponse{Job: genJobPB(created)}), nil
}

func (s *GenerationServer) ListModelEndpoints(ctx context.Context, req *connect.Request[irisv1.ListModelEndpointsRequest]) (*connect.Response[irisv1.ListModelEndpointsResponse], error) {
	resp := &irisv1.ListModelEndpointsResponse{}
	for _, ep := range s.Registry.List(workspaceID(req.Msg.WorkspaceId)) {
		resp.Endpoints = append(resp.Endpoints, &irisv1.ModelEndpoint{
			Id:           ep.ID,
			DisplayName:  ep.DisplayName,
			BaseUrl:      ep.BaseURL,
			Kind:         ep.Kind,
			ManifestJson: string(ep.ManifestRaw),
			Healthy:      ep.Healthy,
		})
	}
	return connect.NewResponse(resp), nil
}

// ── mapping ───────────────────────────────────────────────────────────────────

var stateToPB = map[string]irisv1.JobState{
	"draft":      irisv1.JobState_JOB_STATE_DRAFT,
	"queued":     irisv1.JobState_JOB_STATE_QUEUED,
	"dispatched": irisv1.JobState_JOB_STATE_DISPATCHED,
	"running":    irisv1.JobState_JOB_STATE_RUNNING,
	"uploading":  irisv1.JobState_JOB_STATE_UPLOADING,
	"complete":   irisv1.JobState_JOB_STATE_COMPLETE,
	"failed":     irisv1.JobState_JOB_STATE_FAILED,
	"canceled":   irisv1.JobState_JOB_STATE_CANCELED,
}

func stateString(s irisv1.JobState) string {
	for str, pb := range stateToPB {
		if pb == s {
			return str
		}
	}
	return ""
}

func genJobPB(j *store.GenJob) *irisv1.GenerationJob {
	var genReq store.GenRequest
	_ = json.Unmarshal(j.Request, &genReq)
	pb := &irisv1.GenerationJob{
		Id:                 j.ID,
		WorkspaceId:        j.WorkspaceID,
		ProjectId:          j.ProjectID,
		ModelEndpointId:    j.EndpointID,
		Task:               j.Task,
		Profile:            j.Profile,
		Prompt:             genReq.Prompt,
		NegativePrompt:     genReq.NegativePrompt,
		Seed:               genReq.Seed,
		Count:              int32(genReq.Count),
		TargetEntityId:     j.TargetEntityID,
		DependsOnJobId:     j.DependsOnJobID,
		State:              stateToPB[j.State],
		Progress:           j.Progress,
		ErrorCode:          j.ErrorCode,
		ErrorMessage:       j.ErrorMessage,
		CostEstimate:       j.CostEstimate,
		CostActual:         j.CostActual,
		ArtifactVersionIds: j.ArtifactVersionIDs,
		Timestamps:         ts(j.CreatedAt, j.UpdatedAt),
	}
	if len(genReq.Params) > 0 {
		pb.ParamsJson = string(genReq.Params)
	}
	return pb
}
