package mediaworker

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// ffprobeResult is the subset of `ffprobe -print_format json` output we use.
type ffprobeResult struct {
	Streams []struct {
		CodecType  string `json:"codec_type"`
		Width      int    `json:"width"`
		Height     int    `json:"height"`
		RFrameRate string `json:"r_frame_rate"` // "24/1"
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"` // "2.000000"
	} `json:"format"`
}

type ProbeInfo struct {
	Width, Height int
	DurationS     float64
	FPS           float64
	HasVideo      bool
}

// probeURL runs ffprobe against a (signed) URL and extracts the metadata the
// asset_versions row needs.
func probeURL(ctx context.Context, url string) (*ProbeInfo, error) {
	out, err := exec.CommandContext(ctx, "ffprobe",
		"-v", "quiet", "-print_format", "json", "-show_format", "-show_streams", url).Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe: %w", err)
	}
	return parseProbe(out)
}

func parseProbe(raw []byte) (*ProbeInfo, error) {
	var r ffprobeResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("parse ffprobe output: %w", err)
	}
	info := &ProbeInfo{}
	if d, err := strconv.ParseFloat(r.Format.Duration, 64); err == nil {
		info.DurationS = d
	}
	for _, s := range r.Streams {
		if s.CodecType != "video" {
			continue
		}
		info.HasVideo = true
		info.Width, info.Height = s.Width, s.Height
		info.FPS = parseFrameRate(s.RFrameRate)
		break
	}
	if info.DurationS == 0 && !info.HasVideo {
		return nil, fmt.Errorf("probe found neither duration nor video stream")
	}
	return info, nil
}

// parseFrameRate converts ffprobe's rational "num/den" to a float fps.
func parseFrameRate(r string) float64 {
	num, den, ok := strings.Cut(r, "/")
	if !ok {
		f, _ := strconv.ParseFloat(r, 64)
		return f
	}
	n, err1 := strconv.ParseFloat(num, 64)
	d, err2 := strconv.ParseFloat(den, 64)
	if err1 != nil || err2 != nil || d == 0 {
		return 0
	}
	return n / d
}

// extractPoster grabs a representative frame as JPEG bytes (≤640px wide,
// even dimensions for codec safety).
func extractPoster(ctx context.Context, url string, offsetS float64) ([]byte, error) {
	out, err := exec.CommandContext(ctx, "ffmpeg",
		"-v", "error",
		"-ss", strconv.FormatFloat(offsetS, 'f', 3, 64),
		"-i", url,
		"-frames:v", "1",
		"-vf", "scale='min(640,iw)':-2",
		"-q:v", "3",
		"-f", "image2", "pipe:1").Output()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg poster: %w", err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("ffmpeg produced no poster frame")
	}
	return out, nil
}
