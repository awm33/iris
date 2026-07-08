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
	"fmt"
	"log/slog"
	"strings"
	"time"

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
	claimBatch  = 4
	pollFallback = 15 * time.Second
	jobTimeout  = 2 * time.Minute
)

// Run claims and executes jobs until ctx is canceled.
func (w *Worker) Run(ctx context.Context) error {
	listener, err := queue.NewListener(ctx, w.DSN, queue.MediaChannel, pollFallback)
	if err != nil {
		return fmt.Errorf("listener: %w", err)
	}
	defer listener.Close(context.Background())

	slog.Info("media-worker running", "name", w.Name)
	for ctx.Err() == nil {
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
				slog.Error("listen failed", "err", err)
				time.Sleep(2 * time.Second)
			}
			continue
		}
		for _, job := range jobs {
			w.execute(ctx, job)
		}
	}
	return ctx.Err()
}

func (w *Worker) execute(ctx context.Context, job *queue.MediaJob) {
	jctx, cancel := context.WithTimeout(ctx, jobTimeout)
	defer cancel()

	var err error
	switch job.Kind {
	case "probe":
		err = w.runProbe(jctx, job)
	default:
		err = fmt.Errorf("unknown media job kind %q", job.Kind)
	}
	if err != nil {
		slog.Error("media job failed", "job", job.ID, "kind", job.Kind, "attempt", job.Attempts, "err", err)
		retryable := job.Kind == "probe" // unknown kinds park immediately
		if ferr := queue.FailMediaJob(ctx, w.Pool, job, err, retryable); ferr != nil {
			slog.Error("recording failure failed", "job", job.ID, "err", ferr)
		}
		return
	}
	slog.Info("media job complete", "job", job.ID, "kind", job.Kind)
}

func (w *Worker) runProbe(ctx context.Context, job *queue.MediaJob) error {
	var in probeInput
	if err := json.Unmarshal(job.Input, &in); err != nil || in.VersionID == "" {
		return fmt.Errorf("bad probe input %s: %v", string(job.Input), err)
	}

	var sha, contentType string
	if err := w.Pool.QueryRow(ctx,
		`SELECT sha256, content_type FROM asset_versions WHERE id = $1`, in.VersionID).
		Scan(&sha, &contentType); err != nil {
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

	meta := map[string]any{}
	if info.HasVideo && strings.HasPrefix(contentType, "video/") {
		// Poster from ~10% in (avoids black lead-ins) but never past 1s for
		// short clips; falls back to frame 0 on failure.
		offset := min(info.DurationS*0.1, 1.0)
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

	// Job completion and the probe results commit atomically.
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
		in.VersionID, info.Width, info.Height, info.DurationS, info.FPS, metaJSON); err != nil {
		return fmt.Errorf("update version: %w", err)
	}
	if err := queue.CompleteMediaJob(ctx, tx, job.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
