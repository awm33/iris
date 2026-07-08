// Package orchestrator consumes the generation_jobs queue: for each claimed
// sub-job it resolves references to signed URLs, validates against the
// endpoint's capability manifest, dispatches via the inference API, polls
// with lease heartbeats, and lands artifacts as assets with full lineage —
// artifact rows, job completion, and lineage edges in one transaction.
package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/awm33/iris/backend/internal/blob"
	"github.com/awm33/iris/backend/internal/ids"
	"github.com/awm33/iris/backend/internal/inference"
	"github.com/awm33/iris/backend/internal/queue"
	"github.com/awm33/iris/backend/internal/registry"
	"github.com/awm33/iris/backend/internal/store"
)

type Orchestrator struct {
	Pool     *pgxpool.Pool
	Store    *store.Store
	Blob     *blob.Store
	Registry *registry.Registry
	DSN      string
	Name     string
}

const (
	claimBatch    = 4
	pollFallback  = 15 * time.Second
	reapEvery     = time.Minute
	pollInterval  = 2 * time.Second
	dispatchLimit = 30 * time.Minute // hard ceiling per sub-job incl. queue+generation
	signedURLTTL  = 20 * time.Minute
)

// Run claims and executes generation sub-jobs until ctx is canceled.
// Structure mirrors the media worker (lease reaper, listener reconnect,
// shutdown-safe failure recording).
func (o *Orchestrator) Run(ctx context.Context) error {
	listener, err := queue.NewListener(ctx, o.DSN, queue.GenerationChannel, pollFallback)
	if err != nil {
		return fmt.Errorf("listener: %w", err)
	}
	defer func() { listener.Close(context.Background()) }()

	slog.Info("orchestrator running", "name", o.Name)
	lastReap := time.Time{}
	for ctx.Err() == nil {
		if time.Since(lastReap) > reapEvery {
			if requeued, parked, err := queue.ReapStaleGenerationJobs(ctx, o.Pool); err != nil {
				if ctx.Err() != nil {
					break
				}
				slog.Error("reap failed", "err", err)
			} else if requeued+parked > 0 {
				slog.Warn("reaped stale generation jobs", "requeued", requeued, "parked", parked)
			}
			lastReap = time.Now()
		}

		jobs, err := queue.ClaimGenerationJobs(ctx, o.Pool, o.Name, claimBatch)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			slog.Error("claim failed", "err", err)
			time.Sleep(2 * time.Second)
			continue
		}
		if len(jobs) == 0 {
			if err := listener.Wait(ctx); err != nil && ctx.Err() == nil {
				slog.Error("listener failed, reconnecting", "err", err)
				listener.Close(context.Background())
				time.Sleep(2 * time.Second)
				if nl, lerr := queue.NewListener(ctx, o.DSN, queue.GenerationChannel, pollFallback); lerr == nil {
					listener = nl
				}
			}
			continue
		}
		for _, job := range jobs {
			if ctx.Err() != nil {
				o.recordFailure(job, "transient", "worker shutting down", true)
				continue
			}
			o.execute(ctx, job)
		}
	}
	return ctx.Err()
}

func (o *Orchestrator) execute(ctx context.Context, job *queue.GenerationJob) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("generation job panicked", "job", job.ID, "panic", r)
			o.recordFailure(job, "internal", fmt.Sprintf("panic: %v", r), false)
		}
	}()

	jctx, cancel := context.WithTimeout(ctx, dispatchLimit)
	defer cancel()

	err := o.dispatch(jctx, job)
	if err == nil {
		slog.Info("generation job complete", "job", job.ID)
		return
	}
	if errors.Is(err, queue.ErrNotOwner) {
		slog.Warn("lost generation job lease (or canceled)", "job", job.ID)
		return
	}
	if errors.Is(err, errCanceled) {
		slog.Info("generation job canceled", "job", job.ID)
		return
	}

	code, message, retryable := classify(err)
	slog.Error("generation job failed", "job", job.ID, "attempt", job.Attempts,
		"code", code, "retryable", retryable, "err", err)
	o.recordFailure(job, code, message, retryable)
}

var errCanceled = errors.New("job canceled")

// classify maps errors to the spec's taxonomy (spec §4) for storage on the
// job row and orchestrator retry policy.
func classify(err error) (code, message string, retryable bool) {
	var jerr *inference.JobError
	if errors.As(err, &jerr) {
		switch jerr.Code {
		case "invalid_input", "safety_blocked":
			return jerr.Code, jerr.Message, false
		case "overloaded", "transient":
			return jerr.Code, jerr.Message, true
		default:
			return "internal", jerr.Message, jerr.Retryable
		}
	}
	var verr *inference.ValidationError
	if errors.As(err, &verr) {
		return "invalid_input", verr.Msg, false
	}
	return "transient", err.Error(), true
}

func (o *Orchestrator) recordFailure(job *queue.GenerationJob, code, message string, retryable bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := queue.FailGenerationJob(ctx, o.Pool, job, o.Name, code, message, retryable); err != nil &&
		!errors.Is(err, queue.ErrNotOwner) {
		slog.Error("recording generation failure failed (lease reaper will recover)", "job", job.ID, "err", err)
	}
}

// dispatch runs one sub-job end to end.
func (o *Orchestrator) dispatch(ctx context.Context, job *queue.GenerationJob) error {
	ep, ok := o.Registry.GetOrRefresh(ctx, job.EndpointID)
	if !ok || ep.Manifest == nil {
		return fmt.Errorf("endpoint %s unknown or manifest unavailable", job.EndpointID)
	}

	var req store.GenRequest
	if err := json.Unmarshal(job.Request, &req); err != nil {
		return &inference.ValidationError{Msg: "corrupt job request: " + err.Error()}
	}

	infReq, artifactKey, err := o.buildInferenceRequest(ctx, job, ep, &req)
	if err != nil {
		return err
	}
	// Server-side re-validation against the manifest (the API validated at
	// creation; manifests may have rotated since).
	if err := ep.Manifest.Validate(infReq); err != nil {
		return err
	}

	client := inference.New(ep.BaseURL, ep.Token)
	status, err := client.CreateJob(ctx, infReq)
	if err != nil {
		return err
	}

	// Poll with heartbeats: the heartbeat also detects cancellation (the
	// cancel API flips our row; heartbeat then reports ErrNotOwner). All
	// endpoint-side calls use the per-attempt id (infReq.ID), not job.ID.
	for !status.Terminal() {
		select {
		case <-ctx.Done():
			_ = client.CancelJob(context.Background(), infReq.ID)
			return ctx.Err()
		case <-time.After(pollInterval):
		}
		if err := queue.HeartbeatGenerationJob(ctx, o.Pool, job.ID, o.Name, "running", status.Progress); err != nil {
			if errors.Is(err, queue.ErrNotOwner) {
				_ = client.CancelJob(context.Background(), infReq.ID)
				return errCanceled
			}
			return err
		}
		if status, err = client.GetJob(ctx, infReq.ID); err != nil {
			return err
		}
	}

	switch status.State {
	case "failed":
		if status.Error != nil {
			return status.Error
		}
		return fmt.Errorf("endpoint reported failure without error detail")
	case "canceled":
		return errCanceled
	}

	return o.landArtifact(ctx, job, ep, &req, status, artifactKey)
}

// buildInferenceRequest resolves library references to signed URLs and
// provisions the artifact upload target.
func (o *Orchestrator) buildInferenceRequest(ctx context.Context, job *queue.GenerationJob, ep *registry.Endpoint, req *store.GenRequest) (*inference.CreateJobRequest, string, error) {
	infReq := &inference.CreateJobRequest{
		// Endpoint-side id is unique PER ATTEMPT: terminal states are
		// immutable and creation is idempotent (spec §2–§3), so reusing the
		// id on retry would just fetch the previous attempt's failure.
		ID:             fmt.Sprintf("%s-a%d", job.ID, job.Attempts),
		Task:           job.Task,
		Profile:        job.Profile,
		Prompt:         req.Prompt,
		NegativePrompt: req.NegativePrompt,
		Seed:           req.Seed,
		Params:         req.Params,
	}
	if len(req.Output) > 0 {
		var out inference.Output
		if err := json.Unmarshal(req.Output, &out); err != nil {
			return nil, "", &inference.ValidationError{Msg: "bad output spec: " + err.Error()}
		}
		infReq.Output = &out
	}

	for _, ref := range req.References {
		versionID := ref.VersionID
		if versionID == "" {
			// Float-to-head resolves at dispatch time (HLD pin-vs-float).
			a, _, err := o.Store.GetAsset(ctx, ref.AssetID)
			if err != nil {
				return nil, "", &inference.ValidationError{Msg: fmt.Sprintf("reference asset %s not found", ref.AssetID)}
			}
			versionID = a.HeadVersionID
		}
		info, err := o.Store.GetVersionObjectInfo(ctx, versionID)
		if err != nil {
			return nil, "", &inference.ValidationError{Msg: fmt.Sprintf("reference version %s not found", versionID)}
		}
		url, err := o.Blob.PresignGetExternal(ctx, blob.ContentKey(info.SHA256), info.ContentType, signedURLTTL)
		if err != nil {
			return nil, "", fmt.Errorf("presign reference: %w", err)
		}
		infReq.References = append(infReq.References, inference.Reference{
			Kind: ref.Kind, Role: ref.Role, URL: url, Weight: ref.Weight,
		})
	}

	artifactKey := "uploads/gen/" + job.ID + "/0"
	putURL, err := o.Blob.PresignPutExternal(ctx, artifactKey, signedURLTTL)
	if err != nil {
		return nil, "", fmt.Errorf("presign artifact target: %w", err)
	}
	contentType := "video/mp4"
	if ep.Manifest.Modality == "image" {
		contentType = "image/png"
	}
	infReq.Upload = &inference.Upload{Artifacts: []inference.UploadTarget{{PutURL: putURL, ContentType: contentType}}}
	return infReq, artifactKey, nil
}

// landArtifact promotes the uploaded artifact to content-addressed storage
// and creates the asset + version + lineage + job completion atomically.
func (o *Orchestrator) landArtifact(ctx context.Context, job *queue.GenerationJob, ep *registry.Endpoint, req *store.GenRequest, status *inference.JobStatus, artifactKey string) error {
	if len(status.Artifacts) == 0 || !status.Artifacts[0].Uploaded {
		return fmt.Errorf("endpoint reported complete without an uploaded artifact")
	}
	art := status.Artifacts[0]

	hash, size, _, err := o.Blob.HashAndPromote(ctx, artifactKey)
	if err != nil {
		return fmt.Errorf("promote artifact: %w", err)
	}
	if art.SHA256 != "" && !strings.EqualFold(art.SHA256, hash) {
		return fmt.Errorf("artifact sha256 mismatch: endpoint reported %s, received %s", art.SHA256, hash)
	}

	gpuSeconds := 0.0
	if status.Metrics != nil {
		gpuSeconds = status.Metrics.GPUSeconds
	}

	tx, err := o.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	assetID, versionID := ids.New("ast"), ids.New("astv")
	name := generatedName(req.Prompt, ep.Manifest.Modality)
	kind := "video"
	if ep.Manifest.Modality == "image" {
		kind = "image"
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO assets (id, workspace_id, project_id, kind, name, head_version_id)
		VALUES ($1, $2, NULLIF($3,''), $4, $5, NULL)`,
		assetID, job.WorkspaceID, job.ProjectID, kind, name); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO asset_versions (id, asset_id, sha256, content_type, size_bytes, width, height, duration_s, fps)
		VALUES ($1,$2,$3,$4,$5,NULLIF($6,0),NULLIF($7,0),NULLIF($8,0.0),NULLIF($9,0.0))`,
		versionID, assetID, hash, art.ContentType, size,
		art.Width, art.Height, art.DurationS, art.FPS); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE assets SET head_version_id = $2 WHERE id = $1`, assetID, versionID); err != nil {
		return err
	}
	// Lineage: what made it, and what conditioned it.
	if _, err := tx.Exec(ctx, `
		INSERT INTO asset_links (from_version_id, to_entity_id, role)
		VALUES ($1, $2, 'generated_by')`, versionID, job.ID); err != nil {
		return err
	}
	for _, ref := range req.References {
		if ref.VersionID == "" {
			continue // head-float refs recorded at asset granularity is a follow-up
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO asset_links (from_version_id, to_entity_id, role)
			VALUES ($1, $2, 'reference_of')
			ON CONFLICT DO NOTHING`, ref.VersionID, job.ID); err != nil {
			return err
		}
	}
	// Video artifacts need probe/poster like any other ingest.
	if kind == "video" {
		if err := queue.EnqueueMediaJob(ctx, tx, job.WorkspaceID, "probe",
			map[string]string{"version_id": versionID}); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO usage_events (workspace_id, job_id, kind, unit, quantity)
		VALUES ($1, $2, 'generation', 'gpu_second', $3)`,
		job.WorkspaceID, job.ID, gpuSeconds); err != nil {
		return err
	}
	if err := queue.CompleteGenerationJob(ctx, tx, job.ID, o.Name, gpuSeconds); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func generatedName(prompt, modality string) string {
	p := strings.TrimSpace(prompt)
	if len(p) > 48 {
		p = p[:48] + "…"
	}
	if p == "" {
		p = "untitled"
	}
	return fmt.Sprintf("gen: %s (%s)", p, modality)
}
