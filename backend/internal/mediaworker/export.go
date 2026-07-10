package mediaworker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/awm33/iris/backend/internal/blob"
	"github.com/awm33/iris/backend/internal/ids"
	"github.com/awm33/iris/backend/internal/queue"
	"github.com/awm33/iris/backend/internal/timeline"
)

// Export v1 (M7): replay the persisted op log through the Go reducer, flatten
// to the same pixels the client compositor paints (topmost video track wins,
// gaps black; EVERY audio-bearing clip sounds, overlaps mix), and render one
// H.264+AAC mp4 via an ffmpeg filter graph. The result lands as a normal
// library asset — download, poster, lineage all reuse asset machinery.
//
// Explicitly NOT here (later M7 slices): transitions, color, ducking,
// captions burn-in. Export must match what the preview plays today: cuts.

type exportInput struct {
	ExportID string `json:"export_id"`
}

type exportPreset struct {
	W, H     int
	CRF      string
	X264     string
	AudioKbs string
}

var exportPresets = map[string]exportPreset{
	"draft":  {W: 1280, H: 720, CRF: "28", X264: "veryfast", AudioKbs: "128k"},
	"master": {W: 1920, H: 1080, CRF: "18", X264: "medium", AudioKbs: "192k"},
}

// exportSource is one resolved, downloaded input.
type exportSource struct {
	Path        string
	ContentType string
	HasAudio    bool
}

func (w *Worker) runExport(ctx context.Context, job *queue.MediaJob) error {
	var in exportInput
	if err := json.Unmarshal(job.Input, &in); err != nil || in.ExportID == "" {
		return permanent(fmt.Errorf("bad export input %s: %v", string(job.Input), err))
	}

	var timelineID, presetName, state string
	err := w.Pool.QueryRow(ctx, `
		SELECT timeline_id, preset, state FROM exports WHERE id = $1`, in.ExportID).
		Scan(&timelineID, &presetName, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return permanent(fmt.Errorf("export %s does not exist", in.ExportID))
	}
	if err != nil {
		return err
	}
	if state == "complete" {
		// Redelivered after completion (reaped mid-commit): nothing to
		// render, but the JOB must still complete or it redelivers forever.
		tx, err := w.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err := queue.CompleteMediaJob(ctx, tx, job.ID, w.Name); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	preset, ok := exportPresets[presetName]
	if !ok {
		return w.failExport(ctx, job, in.ExportID, permanent(fmt.Errorf("unknown preset %q", presetName)))
	}
	if _, err := w.Pool.Exec(ctx, `
		UPDATE exports SET state = 'running', error = '', updated_at = now()
		WHERE id = $1`, in.ExportID); err != nil {
		return err
	}
	if err := w.renderExport(ctx, job, in.ExportID, timelineID, presetName, preset); err != nil {
		return w.failExport(ctx, job, in.ExportID, err)
	}
	return nil
}

// failExport records the failure on the row (the UI reads it there) and
// returns the error so the queue's usual retry/park policy still applies —
// a transient failure that later succeeds flips the row back via 'running'.
func (w *Worker) failExport(ctx context.Context, _ *queue.MediaJob, exportID string, err error) error {
	msg := err.Error()
	if len(msg) > 2000 {
		msg = msg[:2000]
	}
	if _, uerr := w.Pool.Exec(ctx, `
		UPDATE exports SET state = 'failed', error = $2, updated_at = now()
		WHERE id = $1`, exportID, msg); uerr != nil {
		return fmt.Errorf("%w (and recording it failed: %v)", err, uerr)
	}
	return err
}

func (w *Worker) renderExport(ctx context.Context, job *queue.MediaJob, exportID, timelineID, presetName string, preset exportPreset) error {
	// 1. Timeline + ops → reduced state.
	var docID, tlName string
	var fps int32
	err := w.Pool.QueryRow(ctx, `
		SELECT doc_id, name, fps FROM timelines WHERE id = $1`, timelineID).
		Scan(&docID, &tlName, &fps)
	if errors.Is(err, pgx.ErrNoRows) {
		return permanent(fmt.Errorf("timeline %s does not exist", timelineID))
	}
	if err != nil {
		return err
	}
	rows, err := w.Pool.Query(ctx, `SELECT op FROM doc_ops WHERE doc_id = $1 ORDER BY seq`, docID)
	if err != nil {
		return err
	}
	var payloads [][]byte
	for rows.Next() {
		var p []byte
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return err
		}
		payloads = append(payloads, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	ops, err := timeline.ParseOps(payloads)
	if err != nil {
		return permanent(fmt.Errorf("op log unreadable: %w", err))
	}
	st := timeline.Reduce(ops)
	totalS := timeline.Duration(st)
	if totalS <= 0 {
		return permanent(errors.New("timeline is empty — nothing to export"))
	}

	// 2. Resolve every clip to a version. Unpicked shots fail loudly and
	// all at once: export is the final render, silent black is worse than
	// a message naming what's missing.
	versionByClip := map[string]string{} // clip id → version id ("" = unresolvable)
	var unresolved []string
	for _, tr := range st.Tracks {
		for _, c := range tr.Clips {
			switch {
			case c.VersionID != "":
				versionByClip[c.ID] = c.VersionID
			case c.ShotID != "":
				var vid string
				err := w.Pool.QueryRow(ctx, `
					SELECT COALESCE(t.version_id, '')
					FROM shots sh JOIN takes t ON t.id = sh.selected_take_id
					WHERE sh.id = $1`, c.ShotID).Scan(&vid)
				if errors.Is(err, pgx.ErrNoRows) || (err == nil && vid == "") {
					unresolved = append(unresolved, fmt.Sprintf("clip %q (shot %s) has no selected take", c.Name, c.ShotID))
					continue
				}
				if err != nil {
					return err
				}
				versionByClip[c.ID] = vid
			}
		}
	}
	if len(unresolved) > 0 {
		return permanent(fmt.Errorf("cannot export: %s", strings.Join(unresolved, "; ")))
	}

	// 3. Download each unique source once (originals — quality decisions
	// live in the preset, not in proxy choice).
	tmpDir, err := os.MkdirTemp("", "iris-export-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	sources := map[string]*exportSource{} // version id → source
	for _, vid := range versionByClip {
		if _, done := sources[vid]; done {
			continue
		}
		src, err := w.fetchExportSource(ctx, tmpDir, vid)
		if err != nil {
			return err
		}
		sources[vid] = src
	}

	// 4. Flatten + audio plan.
	pieces := timeline.Flatten(st, totalS)
	entries := timeline.AudioEntries(st, totalS)

	// 5. Compose + run ffmpeg.
	outPath := tmpDir + "/out.mp4"
	args, err := buildExportArgs(pieces, entries, versionByClip, sources, preset, int(fps), totalS, outPath)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		tail := string(out)
		if len(tail) > 1500 {
			tail = tail[len(tail)-1500:]
		}
		return fmt.Errorf("ffmpeg: %w: %s", err, tail)
	}

	// 6. Land: content-addressed blob + asset/version rows + lineage +
	// probe chain + export completion, atomically with the job.
	data, err := os.ReadFile(outPath)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return errors.New("ffmpeg produced an empty file")
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	if err := w.Blob.PutObject(ctx, blob.ContentKey(sha), "video/mp4",
		bytes.NewReader(data), int64(len(data))); err != nil {
		return fmt.Errorf("store export blob: %w", err)
	}

	tx, err := w.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	assetID, versionID := ids.New("ast"), ids.New("astv")
	name := fmt.Sprintf("Export · %s · %s", tlName, presetName)
	var projectID string
	if err := tx.QueryRow(ctx, `SELECT project_id FROM timelines WHERE id = $1`, timelineID).Scan(&projectID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO assets (id, workspace_id, project_id, kind, name, head_version_id)
		VALUES ($1, $2, $3, 'video', $4, NULL)`,
		assetID, job.WorkspaceID, projectID, name); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO asset_versions (id, asset_id, sha256, content_type, size_bytes)
		VALUES ($1, $2, $3, 'video/mp4', $4)`,
		versionID, assetID, sha, len(data)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE assets SET head_version_id = $2 WHERE id = $1`, assetID, versionID); err != nil {
		return err
	}
	// Lineage: the render came from this timeline (walkable, like
	// generated_by for generation artifacts).
	if _, err := tx.Exec(ctx, `
		INSERT INTO asset_links (from_version_id, to_entity_id, role)
		VALUES ($1, $2, 'exported_from')`, versionID, timelineID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE exports SET state = 'complete', error = '', asset_id = $2, version_id = $3, updated_at = now()
		WHERE id = $1`, exportID, assetID, versionID); err != nil {
		return err
	}
	// Probe chain gives the export its poster/duration in the library.
	if err := queue.EnqueueMediaJob(ctx, tx, job.WorkspaceID, "probe",
		map[string]string{"version_id": versionID}); err != nil {
		return err
	}
	if err := queue.CompleteMediaJob(ctx, tx, job.ID, w.Name); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// fetchExportSource downloads a version's original to tmpDir and ffprobes it
// for an audio stream (the mixer's null-cached no-audio sources, server-side).
func (w *Worker) fetchExportSource(ctx context.Context, tmpDir, versionID string) (*exportSource, error) {
	var sha, contentType string
	err := w.Pool.QueryRow(ctx, `
		SELECT sha256, content_type FROM asset_versions WHERE id = $1`, versionID).
		Scan(&sha, &contentType)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, permanent(fmt.Errorf("version %s does not exist", versionID))
	}
	if err != nil {
		return nil, err
	}
	url, err := w.Blob.PresignGet(ctx, blob.ContentKey(sha), contentType, 30*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("presign %s: %w", versionID, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch source %s: %w", versionID, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch source %s: %d", versionID, res.StatusCode)
	}
	f, err := os.CreateTemp(tmpDir, "src-*")
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(f, res.Body); err != nil {
		f.Close()
		return nil, fmt.Errorf("download source %s: %w", versionID, err)
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		return nil, err
	}

	src := &exportSource{Path: path, ContentType: contentType}
	if strings.HasPrefix(contentType, "video/") || strings.HasPrefix(contentType, "audio/") {
		out, err := exec.CommandContext(ctx, "ffprobe",
			"-v", "error", "-select_streams", "a",
			"-show_entries", "stream=codec_type",
			"-print_format", "json", path).Output()
		if err != nil {
			return nil, permanent(fmt.Errorf("source %s unreadable: %w", versionID, withStderr(err)))
		}
		src.HasAudio = strings.Contains(string(out), `"codec_type"`)
	}
	return src, nil
}
