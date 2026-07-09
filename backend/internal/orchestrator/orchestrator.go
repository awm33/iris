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
	"math"
	"net"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
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
	claimBatch     = 4
	pollFallback   = 15 * time.Second
	reapEvery      = time.Minute
	pollInterval   = 2 * time.Second
	dispatchLimit  = 30 * time.Minute              // hard ceiling per sub-job incl. queue+generation
	refGetTTL      = 20 * time.Minute              // refs are fetched at generation start
	artifactPutTTL = dispatchLimit + 5*time.Minute // must outlive the longest legal generation
	maxGPUSeconds  = 1e6                           // metering sanity ceiling; endpoint-reported values are untrusted
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
		// Concurrent execution: serial batches would starve batch-mates'
		// leases (a video generation legally runs minutes; the lease is 5m),
		// letting a second orchestrator's reaper requeue and double-dispatch.
		var wg sync.WaitGroup
		for _, job := range jobs {
			if ctx.Err() != nil {
				o.recordFailure(job, "transient", "worker shutting down", true)
				continue
			}
			wg.Add(1)
			go func(j *queue.GenerationJob) {
				defer wg.Done()
				o.execute(ctx, j)
			}(job)
		}
		wg.Wait()
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
	var unreachable errUnreachable
	if errors.As(err, &unreachable) {
		slog.Warn("endpoint unreachable; requeueing without attempt cost",
			"job", job.ID, "err", err)
		rctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if rerr := queue.RequeueGenerationJobUnreachable(rctx, o.Pool, job, o.Name, truncateMsg(err.Error())); rerr != nil &&
			!errors.Is(rerr, queue.ErrNotOwner) {
			slog.Error("unreachable requeue failed (lease reaper will recover)", "job", job.ID, "err", rerr)
		}
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
		// Endpoint-supplied text is untrusted and unbounded; cap it before
		// it lands in the job row and API responses.
		msg := truncateMsg(jerr.Message)
		switch jerr.Code {
		case "invalid_input", "safety_blocked":
			return jerr.Code, msg, false
		case "overloaded", "transient":
			return jerr.Code, msg, true
		default:
			return "internal", msg, jerr.Retryable
		}
	}
	var verr *inference.ValidationError
	if errors.As(err, &verr) {
		return "invalid_input", verr.Msg, false
	}
	return "transient", truncateMsg(err.Error()), true
}

func truncateMsg(s string) string {
	if r := []rune(s); len(r) > 500 {
		return string(r[:500]) + "…"
	}
	return s
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

	// Ownership check immediately before the paid dispatch: this job may have
	// been reaped and reclaimed while earlier work in this goroutine ran —
	// dispatching without the lease would double-generate on the endpoint.
	if err := queue.HeartbeatGenerationJob(ctx, o.Pool, job.ID, o.Name, "dispatched", 0); err != nil {
		return err // ErrNotOwner surfaces to execute()
	}

	client := inference.New(ep.BaseURL, ep.Token)
	status, err := client.CreateJob(ctx, infReq)
	if err != nil {
		if isUnreachable(err) {
			return errUnreachable{err}
		}
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
		// Progress is endpoint-reported: clamp before it reaches the DB and
		// the parent's aggregate.
		progress := min(max(status.Progress, 0), 1)
		if err := queue.HeartbeatGenerationJob(ctx, o.Pool, job.ID, o.Name, "running", progress); err != nil {
			if errors.Is(err, queue.ErrNotOwner) {
				_ = client.CancelJob(context.Background(), infReq.ID)
				return errCanceled
			}
			return err
		}
		if status, err = client.GetJob(ctx, infReq.ID); err != nil {
			if isUnreachable(err) {
				return errUnreachable{err}
			}
			return err
		}
	}

	switch status.State {
	case "failed":
		// Failed generations still burned GPU time (spec §3 provides metrics
		// on any terminal state) — meter them or the spend is invisible.
		o.meterUsage(job, status)
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
		url, err := o.Blob.PresignGetExternal(ctx, blob.ContentKey(info.SHA256), info.ContentType, refGetTTL)
		if err != nil {
			return nil, "", fmt.Errorf("presign reference: %w", err)
		}
		infReq.References = append(infReq.References, inference.Reference{
			Kind: ref.Kind, Role: ref.Role, URL: url, Weight: ref.Weight,
		})
	}

	// Per-ATTEMPT key: attempts can overlap (lease expiry → reclaim), and a
	// prior attempt's still-valid PUT URL writing into this attempt's key
	// would corrupt the landed artifact.
	artifactKey := "uploads/gen/" + infReq.ID + "/0"
	putURL, err := o.Blob.PresignPutExternal(ctx, artifactKey, artifactPutTTL)
	if err != nil {
		return nil, "", fmt.Errorf("presign artifact target: %w", err)
	}
	infReq.Upload = &inference.Upload{Artifacts: []inference.UploadTarget{{PutURL: putURL, ContentType: expectedContentType(ep.Manifest.Modality)}}}
	return infReq, artifactKey, nil
}

// expectedContentType is Iris's own decision — the endpoint's reported
// content type is untrusted and never stored (a reported "text/html" would
// otherwise be reflected by SignDownload's response-content-type).
func expectedContentType(modality string) string {
	if modality == "image" {
		return "image/png"
	}
	return "video/mp4"
}

var hexSHA = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// landArtifact promotes the uploaded artifact to content-addressed storage
// and creates the asset + version + lineage + job completion atomically.
//
// Endpoint responses are UNTRUSTED: the stored content type is Iris's own
// (never the endpoint's — a reported "text/html" would be reflected by
// SignDownload), dimensions/duration/fps are left null for the probe worker
// to measure from the actual bytes, and the reported sha256 must be a
// well-formed hash matching what actually arrived.
func (o *Orchestrator) landArtifact(ctx context.Context, job *queue.GenerationJob, ep *registry.Endpoint, req *store.GenRequest, status *inference.JobStatus, artifactKey string) error {
	if len(status.Artifacts) == 0 || !status.Artifacts[0].Uploaded {
		return fmt.Errorf("endpoint reported complete without an uploaded artifact")
	}
	art := status.Artifacts[0]
	if !hexSHA.MatchString(art.SHA256) {
		return &inference.ValidationError{Msg: "endpoint reported malformed artifact sha256"}
	}

	hash, size, _, err := o.Blob.HashAndPromote(ctx, artifactKey)
	if err != nil {
		return fmt.Errorf("promote artifact: %w", err)
	}
	// TODO(follow-up): promoted-object GC — a rollback below (or sha
	// mismatch) strands sha256/<hash> with no referencing row.
	if !strings.EqualFold(art.SHA256, hash) {
		return fmt.Errorf("artifact sha256 mismatch: endpoint reported %s, received %s", art.SHA256, hash)
	}

	gpuSeconds := clampGPUSeconds(status)

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
		INSERT INTO asset_versions (id, asset_id, sha256, content_type, size_bytes)
		VALUES ($1, $2, $3, $4, $5)`,
		versionID, assetID, hash, expectedContentType(ep.Manifest.Modality), size); err != nil {
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
	// Probe for BOTH modalities: it is the sole trusted source of
	// dimensions/duration/fps (and posters for video).
	if err := queue.EnqueueMediaJob(ctx, tx, job.WorkspaceID, "probe",
		map[string]string{"version_id": versionID}); err != nil {
		return err
	}
	// Shot-targeted generations land as Takes — the shot's candidates, with
	// full provenance. Same transaction as the artifact: a take can never
	// reference a version that didn't land.
	//
	// The take block runs under a SAVEPOINT: the shot may have been DELETED
	// while the generation ran (validated only at CreateJob). Failing the
	// whole landing would roll back the artifact AND its usage event, then
	// re-run the paid generation twice more before parking — instead the
	// artifact lands library-only and the spend stays metered.
	if strings.HasPrefix(job.TargetEntityID, "sht_") {
		sub, err := tx.Begin(ctx) // savepoint
		if err != nil {
			return err
		}
		takeID := ids.New("tk")
		recipe, err := json.Marshal(map[string]any{
			"endpoint_id": ep.ID,
			"model":       ep.Manifest.ID,
			"task":        job.Task,
			"profile":     job.Profile,
			"request":     json.RawMessage(job.Request),
		})
		if err != nil {
			recipe = []byte(`{}`) // never fail the paid path over provenance serialization
			slog.Error("recipe marshal failed", "job", job.ID, "err", err)
		}
		landTake := func() error {
			if _, err := sub.Exec(ctx, `
				INSERT INTO takes (id, shot_id, job_id, version_id, quality, recipe)
				VALUES ($1, $2, $3, $4, $5, $6)`,
				takeID, job.TargetEntityID, job.ID, versionID, job.Profile, recipe); err != nil {
				return err
			}
			if _, err := sub.Exec(ctx, `
				INSERT INTO asset_links (from_version_id, to_entity_id, role)
				VALUES ($1, $2, 'used_in_take')`, versionID, takeID); err != nil {
				return err
			}
			// First take auto-selects so the shot shows something
			// immediately; selection stays revisitable in the take picker.
			_, err := sub.Exec(ctx, `
				UPDATE shots SET selected_take_id = $2, updated_at = now()
				WHERE id = $1 AND selected_take_id IS NULL`,
				job.TargetEntityID, takeID)
			return err
		}
		if err := landTake(); err != nil {
			_ = sub.Rollback(ctx)
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23503" {
				slog.Warn("target shot vanished mid-generation; landing artifact library-only",
					"job", job.ID, "shot", job.TargetEntityID)
			} else {
				return err
			}
		} else if err := sub.Commit(ctx); err != nil {
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

// clampGPUSeconds bounds the endpoint-reported metering value — it feeds
// usage_events and cost rollups, so negative or absurd values are rejected.
func clampGPUSeconds(status *inference.JobStatus) float64 {
	if status.Metrics == nil {
		return 0
	}
	g := status.Metrics.GPUSeconds
	if g < 0 || math.IsNaN(g) || math.IsInf(g, 0) {
		slog.Warn("endpoint reported invalid gpu_seconds; metering 0", "value", g)
		return 0
	}
	if g > maxGPUSeconds {
		slog.Warn("endpoint reported implausible gpu_seconds; clamping", "value", g)
		return maxGPUSeconds
	}
	return g
}

// meterUsage records GPU spend for non-complete terminal states (best
// effort — failures here must not mask the job outcome).
func (o *Orchestrator) meterUsage(job *queue.GenerationJob, status *inference.JobStatus) {
	g := clampGPUSeconds(status)
	if g == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := o.Store.RecordUsage(ctx, job.WorkspaceID, job.ID, "generation", "gpu_second", g); err != nil {
		slog.Error("metering failed generation", "job", job.ID, "err", err)
	}
}

func generatedName(prompt, modality string) string {
	p := strings.TrimSpace(prompt)
	if r := []rune(p); len(r) > 48 { // rune-safe: byte slicing can split UTF-8
		p = string(r[:48]) + "…"
	}
	if p == "" {
		p = "untitled"
	}
	return fmt.Sprintf("gen: %s (%s)", p, modality)
}

// errUnreachable marks endpoint-connectivity failures, which requeue without
// burning an attempt (endpoint restarts are routine and outlast 3 backoffs).
type errUnreachable struct{ err error }

func (e errUnreachable) Error() string { return "endpoint unreachable: " + e.err.Error() }
func (e errUnreachable) Unwrap() error { return e.err }

func isUnreachable(err error) bool {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true // dial/timeout/TLS-level failure — no HTTP response at all
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}
