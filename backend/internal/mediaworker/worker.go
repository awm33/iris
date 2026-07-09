// Package mediaworker consumes the media_jobs queue: ingest probe (ffprobe
// metadata → asset_versions) and poster extraction for video. Proxies,
// filmstrips, and waveforms join in M5 per the plan.
//
// ffmpeg/ffprobe are invoked from PATH — containerized in deployment, host
// binaries in local dev.
package mediaworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/awm33/iris/backend/internal/blob"
	"github.com/awm33/iris/backend/internal/queue"
)

type Worker struct {
	Pool *pgxpool.Pool
	Blob *blob.Store
	DSN  string // dedicated LISTEN connection
	Name string
}

type probeInput struct {
	VersionID string `json:"version_id"`
}

const (
	// One job per claim: sequential execution after a batch claim would let
	// slow preps outlive the lease for batch tails (NOTIFY+poll make
	// claim-per-job cheap).
	claimBatch = 1
	pollFallback = 15 * time.Second
	probeTimeout = 2 * time.Minute
	// Prep runs multiple full-decode ffmpeg passes; a 10-min 4K clip needs
	// hundreds of CPU-seconds. Must stay under queue.MediaLease.
	prepTimeout = 15 * time.Minute
	reapEvery    = time.Minute
)

// permanentError marks failures that can never succeed on retry (bad input,
// missing rows) so they park immediately instead of burning attempts.
type permanentError struct{ err error }

func (p permanentError) Error() string { return p.err.Error() }
func (p permanentError) Unwrap() error { return p.err }
func permanent(err error) error        { return permanentError{err} }

// Run claims and executes jobs until ctx is canceled.
func (w *Worker) Run(ctx context.Context) error {
	listener, err := queue.NewListener(ctx, w.DSN, queue.MediaChannel, pollFallback)
	if err != nil {
		return fmt.Errorf("listener: %w", err)
	}
	defer func() { listener.Close(context.Background()) }()

	slog.Info("media-worker running", "name", w.Name)
	lastReap := time.Time{}
	for ctx.Err() == nil {
		if time.Since(lastReap) > reapEvery {
			if requeued, parked, err := queue.ReapStaleMediaJobs(ctx, w.Pool); err != nil {
				if ctx.Err() != nil {
					break
				}
				slog.Error("reap failed", "err", err)
			} else if requeued+parked > 0 {
				slog.Warn("reaped stale jobs", "requeued", requeued, "parked", parked)
			}
			lastReap = time.Now()
		}

		jobs, err := queue.ClaimMediaJobs(ctx, w.Pool, w.Name, claimBatch)
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
				// Connection is unusable (e.g. Postgres restart) — rebuild it;
				// claims keep working through the pool meanwhile.
				slog.Error("listener failed, reconnecting", "err", err)
				listener.Close(context.Background())
				time.Sleep(2 * time.Second)
				if nl, lerr := queue.NewListener(ctx, w.DSN, queue.MediaChannel, pollFallback); lerr == nil {
					listener = nl
				}
			}
			continue
		}
		for _, job := range jobs {
			if ctx.Err() != nil {
				// Shutting down before this job started: requeue it promptly
				// rather than letting the lease reaper find it later.
				w.recordFailure(job, errors.New("worker shutting down"), true)
				continue
			}
			w.execute(ctx, job)
		}
	}
	return ctx.Err()
}

func (w *Worker) execute(ctx context.Context, job *queue.MediaJob) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("media job panicked", "job", job.ID, "kind", job.Kind, "panic", r)
			w.recordFailure(job, fmt.Errorf("panic: %v", r), false)
		}
	}()

	timeout := probeTimeout
	if job.Kind == "prep" {
		timeout = prepTimeout
	}
	jctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var err error
	switch job.Kind {
	case "probe":
		err = w.runProbe(jctx, job)
	case "prep":
		err = w.runPrep(jctx, job)
	default:
		err = permanent(fmt.Errorf("unknown media job kind %q", job.Kind))
	}
	if err != nil {
		if errors.Is(err, queue.ErrNotOwner) {
			// Lease expired mid-job and someone else owns it now; our results
			// were rolled back — nothing to record.
			slog.Warn("lost job lease", "job", job.ID)
			return
		}
		var perm permanentError
		retryable := !errors.As(err, &perm)
		slog.Error("media job failed", "job", job.ID, "kind", job.Kind,
			"attempt", job.Attempts, "retryable", retryable, "err", err)
		w.recordFailure(job, err, retryable)
		return
	}
	slog.Info("media job complete", "job", job.ID, "kind", job.Kind)
}

// recordFailure writes the failure with a context that survives worker
// shutdown — the reviewer-identified stranding path was exactly this write
// failing on the canceled worker context during SIGTERM.
func (w *Worker) recordFailure(job *queue.MediaJob, jobErr error, retryable bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := queue.FailMediaJob(ctx, w.Pool, job, w.Name, jobErr, retryable); err != nil {
		if errors.Is(err, queue.ErrNotOwner) {
			return // reaped already; nothing to do
		}
		slog.Error("recording failure failed (job will be reaped by lease)", "job", job.ID, "err", err)
	}
}

func (w *Worker) runProbe(ctx context.Context, job *queue.MediaJob) error {
	var in probeInput
	if err := json.Unmarshal(job.Input, &in); err != nil || in.VersionID == "" {
		return permanent(fmt.Errorf("bad probe input %s: %v", string(job.Input), err))
	}

	var sha, contentType string
	if err := w.Pool.QueryRow(ctx,
		`SELECT sha256, content_type FROM asset_versions WHERE id = $1`, in.VersionID).
		Scan(&sha, &contentType); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return permanent(fmt.Errorf("version %s does not exist", in.VersionID))
		}
		return fmt.Errorf("load version %s: %w", in.VersionID, err)
	}

	url, err := w.Blob.PresignGet(ctx, blob.ContentKey(sha), "", 10*time.Minute)
	if err != nil {
		return fmt.Errorf("presign source: %w", err)
	}

	info, err := probeURL(ctx, url)
	if err != nil {
		return err
	}

	// Dimensions are trusted for video AND image containers (the probe is the
	// sole metadata source for generated images — endpoint-reported metadata
	// is never stored); audio files with attached cover art also report video
	// streams (parseProbe filters attached_pic, this is the second gate).
	// fps/posters remain video-only.
	isVideo := strings.HasPrefix(contentType, "video/")
	isImage := strings.HasPrefix(contentType, "image/")

	meta := map[string]any{}
	if info.HasVideo && isVideo {
		// Poster from ~10% in (avoids black lead-ins) but never past 1s for
		// short clips; falls back to frame 0 on failure.
		offset := minF(info.DurationS*0.1, 1.0)
		poster, perr := extractPoster(ctx, url, offset)
		if perr != nil {
			poster, perr = extractPoster(ctx, url, 0)
		}
		if perr != nil {
			return perr
		}
		key := "derived/" + in.VersionID + "/poster.jpg"
		if err := w.Blob.PutObject(ctx, key, "image/jpeg", bytes.NewReader(poster), int64(len(poster))); err != nil {
			return fmt.Errorf("store poster: %w", err)
		}
		meta["poster_key"] = key
	}
	metaJSON, _ := json.Marshal(meta)

	width, height, fps := info.Width, info.Height, info.FPS
	if !isVideo && !isImage {
		width, height = 0, 0
	}
	if !isVideo {
		fps = 0
	}

	// Job completion and the probe results commit atomically; the guarded
	// CompleteMediaJob rolls everything back if the lease was lost.
	tx, err := w.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		UPDATE asset_versions SET
			width      = COALESCE(NULLIF($2, 0), width),
			height     = COALESCE(NULLIF($3, 0), height),
			duration_s = COALESCE(NULLIF($4, 0.0), duration_s),
			fps        = COALESCE(NULLIF($5, 0.0), fps),
			meta       = meta || $6
		WHERE id = $1`,
		in.VersionID, width, height, info.DurationS, fps, metaJSON); err != nil {
		return fmt.Errorf("update version: %w", err)
	}
	// Chain the heavier prep pass (proxy/filmstrip/frames/waveform) for
	// playable media — atomically with probe completion.
	if isVideo || strings.HasPrefix(contentType, "audio/") {
		if err := queue.EnqueueMediaJob(ctx, tx, job.WorkspaceID, "prep",
			map[string]string{"version_id": in.VersionID}); err != nil {
			return err
		}
	}
	if err := queue.CompleteMediaJob(ctx, tx, job.ID, w.Name); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
