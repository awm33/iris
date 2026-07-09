package mediaworker

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/awm33/iris/backend/internal/blob"
	"github.com/awm33/iris/backend/internal/queue"
)

// Media prep (M5): everything playback and continuity need, produced once at
// ingest and stored under derived/<version>/… with keys in the version meta:
//
//	proxy_key        720p-max H.264 for WebCodecs scrub/playback (video)
//	filmstrip_key    one JPEG strip of ~50 tiles for timeline clips (video)
//	first_frame_key  full-res PNG (video)
//	last_frame_key   full-res PNG — the carry-last-frame-to-next-shot input
//	waveform_key     JSON peaks (0..1, ~1000 buckets) (any audio stream)
//
// Failures park the job with the reaper's usual policy; the asset stays
// usable (players fall back to originals; strips fall back to posters).

const (
	filmstripTiles  = 50
	filmstripTileH  = 100
	waveformBuckets = 1000
)

func (w *Worker) runPrep(ctx context.Context, job *queue.MediaJob) error {
	var in probeInput
	if err := json.Unmarshal(job.Input, &in); err != nil || in.VersionID == "" {
		return permanent(fmt.Errorf("bad prep input %s: %v", string(job.Input), err))
	}

	var sha, contentType string
	var durationS float64
	if err := w.Pool.QueryRow(ctx, `
		SELECT sha256, content_type, COALESCE(duration_s, 0)
		FROM asset_versions WHERE id = $1`, in.VersionID).
		Scan(&sha, &contentType, &durationS); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return permanent(fmt.Errorf("version %s does not exist", in.VersionID))
		}
		return fmt.Errorf("load version %s: %w", in.VersionID, err)
	}
	isVideo := strings.HasPrefix(contentType, "video/")
	isAudio := strings.HasPrefix(contentType, "audio/")
	if !isVideo && !isAudio {
		return permanent(fmt.Errorf("prep on non-media version %s (%s)", in.VersionID, contentType))
	}

	url, err := w.Blob.PresignGet(ctx, blob.ContentKey(sha), "", 30*time.Minute)
	if err != nil {
		return fmt.Errorf("presign source: %w", err)
	}

	meta := map[string]any{}
	put := func(key, ct string, data []byte) error {
		return w.Blob.PutObject(ctx, key, ct, bytes.NewReader(data), int64(len(data)))
	}
	prefix := "derived/" + in.VersionID + "/"

	if isVideo {
		proxy, err := transcodeProxy(ctx, url)
		if err != nil {
			return err
		}
		if err := put(prefix+"proxy.mp4", "video/mp4", proxy); err != nil {
			return fmt.Errorf("store proxy: %w", err)
		}
		meta["proxy_key"] = prefix + "proxy.mp4"

		strip, cols, err := extractFilmstrip(ctx, url, durationS)
		if err != nil {
			return err
		}
		if err := put(prefix+"filmstrip.jpg", "image/jpeg", strip); err != nil {
			return fmt.Errorf("store filmstrip: %w", err)
		}
		meta["filmstrip_key"] = prefix + "filmstrip.jpg"
		meta["filmstrip_cols"] = cols

		first, err := extractFrame(ctx, url, 0, false)
		if err != nil {
			return err
		}
		if err := put(prefix+"first.png", "image/png", first); err != nil {
			return fmt.Errorf("store first frame: %w", err)
		}
		meta["first_frame_key"] = prefix + "first.png"

		last, err := extractFrame(ctx, url, 0, true)
		if err != nil {
			return err
		}
		if err := put(prefix+"last.png", "image/png", last); err != nil {
			return fmt.Errorf("store last frame: %w", err)
		}
		meta["last_frame_key"] = prefix + "last.png"
	}

	// Waveform for any audio stream (video soundtracks included). A missing
	// audio stream is not an error — probe can't cheaply tell us, so ffmpeg's
	// "no audio" failure is treated as "nothing to do".
	if peaks, err := extractWaveform(ctx, url); err == nil && len(peaks) > 0 {
		wf, _ := json.Marshal(map[string]any{"buckets": len(peaks), "peaks": peaks})
		if err := put(prefix+"waveform.json", "application/json", wf); err != nil {
			return fmt.Errorf("store waveform: %w", err)
		}
		meta["waveform_key"] = prefix + "waveform.json"
	} else if isAudio {
		// Pure audio without an extractable waveform IS a failure.
		if err != nil {
			return err
		}
	}

	metaJSON, _ := json.Marshal(meta)
	tx, err := w.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`UPDATE asset_versions SET meta = meta || $2 WHERE id = $1`,
		in.VersionID, metaJSON); err != nil {
		return fmt.Errorf("update version: %w", err)
	}
	if err := queue.CompleteMediaJob(ctx, tx, job.ID, w.Name); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// transcodeProxy renders a 720p-max H.264+AAC preview. faststart needs a
// seekable output, so this goes through a temp file rather than a pipe.
func transcodeProxy(ctx context.Context, url string) ([]byte, error) {
	tmp, err := os.CreateTemp("", "iris-proxy-*.mp4")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(tmpPath)

	if _, err := exec.CommandContext(ctx, "ffmpeg",
		"-v", "error", "-y",
		"-protocol_whitelist", protocolWhitelist,
		"-i", url,
		"-vf", "scale='min(1280,iw)':-2",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "26",
		"-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "128k",
		"-movflags", "+faststart",
		tmpPath).Output(); err != nil {
		return nil, fmt.Errorf("ffmpeg proxy: %w", withStderr(err))
	}
	return os.ReadFile(tmpPath)
}

// extractFilmstrip tiles evenly-spaced frames into one horizontal JPEG.
func extractFilmstrip(ctx context.Context, url string, durationS float64) ([]byte, int, error) {
	cols := filmstripTiles
	if durationS > 0 && durationS < float64(cols) {
		// Short clips: at least ~1 tile/second, minimum 2.
		cols = max(2, int(durationS))
	}
	fps := float64(cols) / maxF(durationS, 0.5)
	out, err := exec.CommandContext(ctx, "ffmpeg",
		"-v", "error",
		"-protocol_whitelist", protocolWhitelist,
		"-i", url,
		"-vf", fmt.Sprintf("fps=%f,scale=-2:%d,tile=%dx1", fps, filmstripTileH, cols),
		"-frames:v", "1", "-q:v", "4",
		"-f", "image2", "pipe:1").Output()
	if err != nil {
		return nil, 0, fmt.Errorf("ffmpeg filmstrip: %w", withStderr(err))
	}
	if len(out) == 0 {
		return nil, 0, fmt.Errorf("ffmpeg produced no filmstrip")
	}
	return out, cols, nil
}

// extractFrame grabs the first or last frame at full resolution as PNG.
// The last frame is the continuity input for shot-to-shot carry (HLD W3).
func extractFrame(ctx context.Context, url string, offsetS float64, last bool) ([]byte, error) {
	args := []string{"-v", "error", "-protocol_whitelist", protocolWhitelist}
	if last {
		args = append(args, "-sseof", "-0.2")
	} else if offsetS > 0 {
		args = append(args, "-ss", strconv.FormatFloat(offsetS, 'f', 3, 64))
	}
	args = append(args, "-i", url, "-frames:v", "1", "-update", "1", "-f", "image2", "-c:v", "png", "pipe:1")
	out, err := exec.CommandContext(ctx, "ffmpeg", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg frame: %w", withStderr(err))
	}
	if len(out) == 0 && last {
		// -sseof can undershoot very short clips; fall back to frame 0.
		return extractFrame(ctx, url, 0, false)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("ffmpeg produced no frame")
	}
	return out, nil
}

// extractWaveform downmixes to 8kHz mono PCM and reduces to ~1000 peak
// buckets (max |amplitude| per bucket, 0..1). 8kHz keeps envelope fidelity —
// near-Nyquist resampling aliases the envelope (a constant 440Hz tone at
// 1kHz sampling showed a 3x peak wobble in testing; at 8kHz it is flat).
func extractWaveform(ctx context.Context, url string) ([]float64, error) {
	out, err := exec.CommandContext(ctx, "ffmpeg",
		"-v", "error",
		"-protocol_whitelist", protocolWhitelist,
		"-i", url,
		"-map", "0:a:0",
		"-ac", "1", "-ar", "8000",
		"-c:a", "pcm_s16le", "-f", "s16le", "pipe:1").Output()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg waveform: %w", withStderr(err))
	}
	samples := len(out) / 2
	if samples == 0 {
		return nil, nil
	}
	buckets := waveformBuckets
	if samples < buckets {
		buckets = samples
	}
	peaks := make([]float64, buckets)
	per := samples / buckets
	for b := 0; b < buckets; b++ {
		var peak int16
		for i := b * per; i < (b+1)*per && i < samples; i++ {
			v := int16(binary.LittleEndian.Uint16(out[i*2:]))
			if v < 0 {
				v = -v
			}
			if v > peak {
				peak = v
			}
		}
		peaks[b] = float64(peak) / 32767.0
	}
	return peaks, nil
}

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
