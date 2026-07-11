package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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

const (
	maxFanout        = 8
	maxPromptLen     = 10_000 // runes; stored per sub-job and forwarded to endpoints
	maxParamsJSONLen = 64 << 10
)

func (s *GenerationServer) CreateJob(ctx context.Context, req *connect.Request[irisv1.CreateJobRequest]) (*connect.Response[irisv1.CreateJobResponse], error) {
	created, err := s.createJobMsg(ctx, req.Msg.Job)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&irisv1.CreateJobResponse{Job: genJobPB(created)}), nil
}

// createJobMsg is CreateJob's core, shared with RegenerateChain (which
// constructs one job message per chain link).
func (s *GenerationServer) createJobMsg(ctx context.Context, j *irisv1.GenerationJob) (*store.GenJob, error) {
	if j == nil || j.ModelEndpointId == "" || j.Task == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("job.model_endpoint_id and job.task are required"))
	}
	// Canonicalize: removal keys on EXACTLY "" downstream (naming, endpoint
	// contract) — a whitespace-only prompt must not be named "removal" while
	// behaving as generation.
	j.Prompt = strings.TrimSpace(j.Prompt)
	// Empty prompt is a REMOVAL for inpaint (spec §2: reconstruct background,
	// insert nothing). lipsync_post re-times an existing clip to audio —
	// there is nothing to prompt. Every other task needs one.
	if j.Prompt == "" && j.Task != "inpaint" && j.Task != "lipsync_post" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("job.prompt is required"))
	}
	if j.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("job.project_id is required"))
	}
	if len([]rune(j.Prompt)) > maxPromptLen || len([]rune(j.NegativePrompt)) > maxPromptLen {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("prompt too long"))
	}
	if len(j.ParamsJson) > maxParamsJSONLen {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("params_json too large"))
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
	if !ok || ep.WorkspaceID != workspaceID(j.WorkspaceId) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("unknown model endpoint"))
	}
	if !ep.Healthy || ep.Manifest == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("endpoint unhealthy or manifest unavailable"))
	}
	if err := s.validateTarget(ctx, j.TargetEntityId, j.ProjectId); err != nil {
		return nil, err
	}
	// A dependency that can never complete would strand this job (dependents
	// gate on 'complete'); reject up front. Post-create failures propagate
	// via FailDependents.
	if j.DependsOnJobId != "" {
		dep, err := s.Store.GetGenJob(ctx, j.DependsOnJobId)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("depends_on_job_id not found"))
		}
		if dep.State == "failed" || dep.State == "canceled" {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				errors.New("dependency job is "+dep.State+" and will never complete"))
		}
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
	if _, err := s.Store.CreateGenerationFanout(ctx, parent, count, ep.Manifest.Features.Seed); err != nil {
		return nil, connectErr(err)
	}
	created, err := s.Store.GetGenJob(ctx, parent.ID)
	if err != nil {
		return nil, connectErr(err)
	}
	return created, nil
}

// validateTarget checks a generation target: it must exist AND belong to the
// job's project (the artifact lands in the job's project; a cross-project
// target would silently mutate another project's entity). Shots get Takes at
// landing; canvas targets land library-only — the canvas references the
// versions via layer ops, so the target is provenance + UI routing.
func (s *GenerationServer) validateTarget(ctx context.Context, targetEntityID, projectID string) error {
	if targetEntityID == "" {
		return nil
	}
	switch {
	case strings.HasPrefix(targetEntityID, "sht_"):
		shotProject, err := s.Store.ShotProjectID(ctx, targetEntityID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return connect.NewError(connect.CodeInvalidArgument, errors.New("target shot not found"))
			}
			return connectErr(err)
		}
		if shotProject != projectID {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("target shot belongs to a different project"))
		}
		return nil
	case strings.HasPrefix(targetEntityID, "cnv_"):
		c, err := s.Store.GetCanvas(ctx, targetEntityID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return connect.NewError(connect.CodeInvalidArgument, errors.New("target canvas not found"))
			}
			return connectErr(err)
		}
		if c.ProjectID != projectID {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("target canvas belongs to a different project"))
		}
		return nil
	default:
		return connect.NewError(connect.CodeInvalidArgument, errors.New("unsupported target entity type"))
	}
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
		// A marshal failure (NaN/Inf reach here via proto3 JSON doubles)
		// must reject the request — silently dropping the validated output
		// block would dispatch the job without its dimensions.
		outJSON, err := json.Marshal(out)
		if err != nil {
			return nil, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("output contains non-finite numbers"))
		}
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
	// Conditioning: gen-fill (M4) uses source_image + mask; continuity carry
	// (M5) uses first_frame. The remaining spec keys (last_frame/keyframes/
	// depth/source_video) wire up with the surfaces that need them; the
	// proto carries them but they are rejected here rather than silently
	// dropped.
	if c := j.Conditioning; c != nil {
		if c.LastFrame != nil || len(c.Keyframes) > 0 || c.DepthSequence != nil {
			return nil, nil, connect.NewError(connect.CodeUnimplemented,
				errors.New("only first_frame, source_video, source_image and mask conditioning are supported so far"))
		}
		if c.FirstFrame != nil || c.SourceImage != nil || c.Mask != nil || c.SourceVideo != nil {
			genReq.Conditioning = &store.GenConditioning{}
			infReq.Conditioning = &inference.Conditioning{}
			if c.FirstFrame != nil {
				if c.FirstFrame.AssetId == "" && c.FirstFrame.VersionId == "" {
					return nil, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("conditioning.first_frame missing asset or version"))
				}
				genReq.Conditioning.FirstFrame = &store.GenRef{
					AssetID: c.FirstFrame.AssetId, VersionID: c.FirstFrame.VersionId,
				}
				infReq.Conditioning.FirstFrame = &inference.FrameRef{}
			}
			if c.SourceVideo != nil {
				if c.SourceVideo.AssetId == "" && c.SourceVideo.VersionId == "" {
					return nil, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("conditioning.source_video missing asset or version"))
				}
				genReq.Conditioning.SourceVideo = &store.GenRef{
					AssetID: c.SourceVideo.AssetId, VersionID: c.SourceVideo.VersionId,
				}
				infReq.Conditioning.SourceVideo = &inference.SourceVideo{}
			}
			if c.SourceImage != nil {
				if c.SourceImage.AssetId == "" {
					return nil, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("conditioning.source_image missing asset"))
				}
				genReq.Conditioning.SourceImage = &store.GenRef{
					AssetID: c.SourceImage.AssetId, VersionID: c.SourceImage.VersionId,
				}
				// URL filled at dispatch; validation only needs presence.
				infReq.Conditioning.SourceImage = &inference.FrameRef{}
			}
			if c.Mask != nil {
				if c.Mask.AssetId == "" {
					return nil, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("conditioning.mask missing asset"))
				}
				genReq.Conditioning.Mask = &store.GenRef{
					AssetID: c.Mask.AssetId, VersionID: c.Mask.VersionId,
				}
				infReq.Conditioning.Mask = &inference.FrameRef{}
			}
		}
	}
	if j.Task == "lipsync_post" && (genReq.Conditioning == nil || genReq.Conditioning.SourceVideo == nil) {
		return nil, nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("lipsync_post requires conditioning.source_video (the clip to re-time)"))
	}
	if j.Task == "inpaint" && (genReq.Conditioning == nil ||
		genReq.Conditioning.SourceImage == nil || genReq.Conditioning.Mask == nil) {
		return nil, nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("inpaint requires conditioning.source_image and conditioning.mask"))
	}
	if j.CarryFromDependsOn {
		if j.DependsOnJobId == "" {
			return nil, nil, connect.NewError(connect.CodeInvalidArgument,
				errors.New("carry_from_depends_on requires depends_on_job_id"))
		}
		if genReq.Conditioning != nil && genReq.Conditioning.FirstFrame != nil {
			return nil, nil, connect.NewError(connect.CodeInvalidArgument,
				errors.New("carry_from_depends_on and conditioning.first_frame are mutually exclusive"))
		}
		genReq.CarryFromDependsOn = true
		if infReq.Conditioning == nil {
			infReq.Conditioning = &inference.Conditioning{}
		}
		// Placeholder so the manifest capability check runs at CREATE time —
		// the real URL resolves at dispatch from the dependency's artifact.
		infReq.Conditioning.FirstFrame = &inference.FrameRef{}
	}
	return genReq, infReq, nil
}

// RegenerateChain (W3): one count=1 job per chained shot from start_shot_id
// forward in scene order — the first carries from the CURRENT upstream
// selected take, each subsequent from its dependency's landed artifact.
// The chain stops at a pinned shot (frozen by the user) or the first shot
// that wasn't itself generated with a carry (unchained).
func (s *GenerationServer) RegenerateChain(ctx context.Context, req *connect.Request[irisv1.RegenerateChainRequest]) (*connect.Response[irisv1.RegenerateChainResponse], error) {
	if req.Msg.StartShotId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("start_shot_id is required"))
	}
	start, err := s.Store.GetShot(ctx, req.Msg.StartShotId)
	if err != nil {
		return nil, connectErr(err)
	}
	if start.Pinned {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("start shot is pinned"))
	}
	sc, _, shots, err := s.Store.GetScene(ctx, start.SceneID)
	if err != nil {
		return nil, connectErr(err)
	}

	// Locate the start by IDENTITY in the scene snapshot — positions can
	// duplicate (the board renumbers non-atomically), and GetShot/GetScene
	// are two reads: matching by position would walk the wrong shots or
	// miss the start entirely.
	startIdx := -1
	for i, sh := range shots {
		if sh.ID == start.ID {
			startIdx = i
			break
		}
	}
	if startIdx == -1 {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("start shot vanished from its scene"))
	}

	// The chain: start shot, then following shots whose selected take was
	// generated WITH a carry (recipe provenance), stopping at pins.
	type link struct{ shot *store.ShotRow }
	var chain []link
	for _, sh := range shots[startIdx:] {
		if sh.ID != start.ID {
			if sh.Pinned {
				break
			}
			chained, err := s.Store.TakeWasCarried(ctx, sh.SelectedTakeID)
			if err != nil {
				return nil, connectErr(err) // a DB blip must not read as "unchained"
			}
			if !chained {
				break
			}
		}
		if sh.SelectedTakeID == "" {
			break // nothing to replay
		}
		chain = append(chain, link{shot: sh})
	}
	if len(chain) == 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("no regenerable shots in the chain"))
	}

	// Upstream carry input for the FIRST link: nearest earlier shot with a
	// selected take (matches the generate panel's semantics).
	var firstCarry string
	for _, sh := range shots[:startIdx] {
		if sh.SelectedTakeVersionID != "" {
			firstCarry = sh.SelectedTakeVersionID
		}
	}

	resp := &irisv1.RegenerateChainResponse{}
	prevJobID := ""
	for i, l := range chain {
		j, err := s.jobFromTakeRecipe(ctx, l.shot, sc.ProjectID)
		if err != nil {
			return nil, err
		}
		j.Count = 1
		j.Seed = 0 // fresh takes
		if i == 0 {
			if firstCarry != "" {
				j.Conditioning = &irisv1.Conditioning{FirstFrame: &irisv1.AssetRef{VersionId: firstCarry}}
			}
		} else {
			j.DependsOnJobId = prevJobID
			j.CarryFromDependsOn = true
			j.Conditioning = nil
		}
		created, cerr := s.createJobMsg(ctx, j)
		if cerr != nil {
			// Mid-chain failure: earlier links already run — report what
			// was created rather than pretending atomicity we don't have,
			// but SAY the walk was cut short (silent truncation reads as a
			// legitimately shorter chain).
			if len(resp.JobIds) > 0 {
				resp.Error = cerr.Error()
				resp.FailedShotId = l.shot.ID
				return connect.NewResponse(resp), nil
			}
			return nil, cerr
		}
		prevJobID = created.ID
		resp.JobIds = append(resp.JobIds, created.ID)
	}
	return connect.NewResponse(resp), nil
}

// jobFromTakeRecipe rebuilds a CreateJob message from a shot's selected
// take's recipe — the replayable provenance (endpoint, task, profile,
// prompt, output, references).
func (s *GenerationServer) jobFromTakeRecipe(ctx context.Context, sh *store.ShotRow, projectID string) (*irisv1.GenerationJob, error) {
	recipe, err := s.Store.TakeRecipe(ctx, sh.SelectedTakeID)
	if err != nil {
		return nil, connectErr(err)
	}
	var r struct {
		EndpointID string `json:"endpoint_id"`
		Task       string `json:"task"`
		Profile    string `json:"profile"`
		Request    struct {
			Prompt         string          `json:"prompt"`
			NegativePrompt string          `json:"negative_prompt"`
			Output         json.RawMessage `json:"output"`
			References     []store.GenRef  `json:"references"`
			Params         json.RawMessage `json:"params"`
			Conditioning   *struct {
				SourceVideo *store.GenRef `json:"source_video"`
				SourceImage *store.GenRef `json:"source_image"`
				Mask        *store.GenRef `json:"mask"`
			} `json:"conditioning"`
		} `json:"request"`
	}
	if err := json.Unmarshal(recipe, &r); err != nil || r.EndpointID == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("shot %s's selected take has no replayable recipe", sh.ID))
	}
	j := &irisv1.GenerationJob{
		ProjectId:       projectID,
		ModelEndpointId: r.EndpointID,
		Task:            r.Task,
		Profile:         r.Profile,
		Prompt:          r.Request.Prompt,
		NegativePrompt:  r.Request.NegativePrompt,
		TargetEntityId:  sh.ID,
	}
	if len(r.Request.Params) > 0 && string(r.Request.Params) != "null" {
		// Dropping params would silently replay with different generation
		// semantics (guidance, motion) than the take being regenerated.
		j.ParamsJson = string(r.Request.Params)
	}
	// Replay conditioning the task depends on — a lipsync_post recipe
	// without its source_video can't even be created. (first_frame is
	// deliberately NOT replayed here: chain regeneration re-derives the
	// carry from the current upstream.)
	if c := r.Request.Conditioning; c != nil {
		toRef := func(g *store.GenRef) *irisv1.AssetRef {
			if g == nil {
				return nil
			}
			return &irisv1.AssetRef{AssetId: g.AssetID, VersionId: g.VersionID}
		}
		if c.SourceVideo != nil || c.SourceImage != nil || c.Mask != nil {
			j.Conditioning = &irisv1.Conditioning{
				SourceVideo: toRef(c.SourceVideo),
				SourceImage: toRef(c.SourceImage),
				Mask:        toRef(c.Mask),
			}
		}
	}
	if len(r.Request.Output) > 0 {
		var out struct {
			Width     int32   `json:"width"`
			Height    int32   `json:"height"`
			DurationS float64 `json:"duration_s"`
			FPS       float64 `json:"fps"`
		}
		if json.Unmarshal(r.Request.Output, &out) == nil {
			j.Output = &irisv1.OutputSpec{Width: out.Width, Height: out.Height, DurationS: out.DurationS, Fps: out.FPS}
		}
	}
	for _, ref := range r.Request.References {
		j.References = append(j.References, &irisv1.Reference{
			Kind: ref.Kind, Role: ref.Role, Weight: ref.Weight,
			Asset: &irisv1.AssetRef{AssetId: ref.AssetID, VersionId: ref.VersionID},
		})
	}
	return j, nil
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
	// the per-attempt id only it knows. Canceling a terminal job is a no-op.
	if err := s.Store.CancelGeneration(ctx, req.Msg.Id); err != nil {
		return nil, connectErr(err)
	}
	return connect.NewResponse(&irisv1.CancelJobResponse{}), nil
}

func (s *GenerationServer) RetryJob(ctx context.Context, req *connect.Request[irisv1.RetryJobRequest]) (*connect.Response[irisv1.RetryJobResponse], error) {
	prev, err := s.Store.GetGenJob(ctx, req.Msg.Id)
	if err != nil {
		return nil, connectErr(err)
	}
	// Guardrails: only terminal parents are retryable — retrying a running
	// job silently doubles its whole fan-out and spend; retrying a sub-job
	// builds a nonsensical one-sub parent from a child row.
	if prev.ParentJobID != "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("retry the parent job, not a sub-job"))
	}
	switch prev.State {
	case "failed", "canceled":
	default:
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("only failed or canceled jobs can be retried (state: "+prev.State+")"))
	}
	// A dependency that terminally failed would strand the retry immediately.
	if prev.DependsOnJobID != "" {
		if dep, err := s.Store.GetGenJob(ctx, prev.DependsOnJobID); err == nil &&
			(dep.State == "failed" || dep.State == "canceled") {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				errors.New("dependency job is "+dep.State+"; retry it first"))
		}
	}
	// The target shot may have been deleted since the original job.
	if err := s.validateTarget(ctx, prev.TargetEntityID, prev.ProjectID); err != nil {
		return nil, err
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
	// Same seed-resolution rule as CreateJob; an endpoint gone from the
	// registry resolves conservatively (seeds stay random-at-endpoint).
	resolveSeeds := false
	if ep, ok := s.Registry.Get(prev.EndpointID); ok && ep.Manifest != nil {
		resolveSeeds = ep.Manifest.Features.Seed
	}
	if _, err := s.Store.CreateGenerationFanout(ctx, parent, count, resolveSeeds); err != nil {
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
		TargetLabel:        j.TargetLabel,
		TargetSceneId:      j.TargetSceneID,
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
	// Echo the full recipe: a client must be able to reconstruct (and
	// re-submit) a job from GetJob alone.
	for _, r := range genReq.References {
		pb.References = append(pb.References, &irisv1.Reference{
			Kind: r.Kind, Role: r.Role, Weight: r.Weight,
			Asset: &irisv1.AssetRef{AssetId: r.AssetID, VersionId: r.VersionID},
		})
	}
	pb.CarryFromDependsOn = genReq.CarryFromDependsOn
	if c := genReq.Conditioning; c != nil {
		pb.Conditioning = &irisv1.Conditioning{}
		if c.FirstFrame != nil {
			pb.Conditioning.FirstFrame = &irisv1.AssetRef{AssetId: c.FirstFrame.AssetID, VersionId: c.FirstFrame.VersionID}
		}
		if c.SourceImage != nil {
			pb.Conditioning.SourceImage = &irisv1.AssetRef{AssetId: c.SourceImage.AssetID, VersionId: c.SourceImage.VersionID}
		}
		if c.SourceVideo != nil {
			pb.Conditioning.SourceVideo = &irisv1.AssetRef{AssetId: c.SourceVideo.AssetID, VersionId: c.SourceVideo.VersionID}
		}
		if c.Mask != nil {
			pb.Conditioning.Mask = &irisv1.AssetRef{AssetId: c.Mask.AssetID, VersionId: c.Mask.VersionID}
		}
	}
	return pb
}
