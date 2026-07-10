package mediaworker

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/awm33/iris/backend/internal/timeline"
)

// buildExportArgs assembles the full ffmpeg invocation for an export: one
// input per video piece / audio entry (no split plumbing — input counts are
// dogfood-scale), a filter graph that normalizes every piece to the preset's
// frame (fps/scale/pad), concats video, and mixes audio.
//
// Duration exactness is load-bearing: a clip whose duration outruns its
// source must freeze on the last frame like the preview does (tpad clone),
// never shorten — a shortened piece slides every later cut and desyncs the
// mix. Same for audio (apad). trim/atrim then cut to the exact piece length.
func buildExportArgs(
	pieces []timeline.Piece,
	entries []timeline.AudioEntry,
	versionByClip map[string]string,
	sources map[string]*exportSource,
	p exportPreset,
	fps int,
	totalS float64,
	outPath string,
) ([]string, error) {
	if fps <= 0 {
		fps = 24
	}
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', 6, 64) }
	scaleChain := fmt.Sprintf(
		"fps=%d,scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1,format=yuv420p",
		fps, p.W, p.H, p.W, p.H)

	var inputs []string
	var filters []string
	nInput := 0

	srcFor := func(clip *timeline.Clip) *exportSource {
		vid := versionByClip[clip.ID]
		if vid == "" {
			return nil
		}
		return sources[vid]
	}

	// Video pieces → [v0..vN], in timeline order.
	var vLabels []string
	for i, piece := range pieces {
		label := fmt.Sprintf("v%d", i)
		src := (*exportSource)(nil)
		if piece.Clip != nil {
			src = srcFor(piece.Clip)
		}
		switch {
		case src != nil && strings.HasPrefix(src.ContentType, "video/"):
			inputs = append(inputs,
				"-ss", f(piece.Clip.InPoint), "-t", f(piece.Duration), "-i", src.Path)
			filters = append(filters, fmt.Sprintf(
				"[%d:v]%s,tpad=stop_mode=clone:stop_duration=%s,trim=end=%s,setpts=PTS-STARTPTS[%s]",
				nInput, scaleChain, f(piece.Duration), f(piece.Duration), label))
			nInput++
		case src != nil && strings.HasPrefix(src.ContentType, "image/"):
			// Stills (image takes) hold the frame for the piece duration —
			// the preview's overlay semantics.
			inputs = append(inputs, "-loop", "1", "-t", f(piece.Duration), "-i", src.Path)
			filters = append(filters, fmt.Sprintf(
				"[%d:v]%s,trim=end=%s,setpts=PTS-STARTPTS[%s]",
				nInput, scaleChain, f(piece.Duration), label))
			nInput++
		default:
			// Gap — or a clip whose source can't paint (audio on a video
			// track can't happen via the UI; render black over failing).
			filters = append(filters, fmt.Sprintf(
				"color=black:s=%dx%d:r=%d:d=%s,format=yuv420p[%s]",
				p.W, p.H, fps, f(piece.Duration), label))
		}
		vLabels = append(vLabels, "["+label+"]")
	}
	if len(vLabels) == 0 {
		return nil, fmt.Errorf("no video pieces to render")
	}
	filters = append(filters, fmt.Sprintf("%sconcat=n=%d:v=1:a=0[vout]",
		strings.Join(vLabels, ""), len(vLabels)))

	// Audio entries → [a0..aK] → amix. Silence-only timelines still get a
	// track: players expect one, and the mix keeps A/V duration equal.
	var aLabels []string
	for _, e := range entries {
		src := srcFor(e.Clip)
		if src == nil || !src.HasAudio {
			continue // stills, silent sources — the mixer's null-cached skip
		}
		label := fmt.Sprintf("a%d", len(aLabels))
		inputs = append(inputs,
			"-ss", f(e.Clip.InPoint), "-t", f(e.Dur), "-i", src.Path)
		filters = append(filters, fmt.Sprintf(
			"[%d:a]aresample=48000,aformat=sample_fmts=fltp:channel_layouts=stereo,"+
				"apad=pad_dur=%s,atrim=end=%s,asetpts=PTS-STARTPTS,adelay=%d:all=1[%s]",
			nInput, f(e.Dur), f(e.Dur), int(e.Start*1000+0.5), label))
		nInput++
		aLabels = append(aLabels, "["+label+"]")
	}
	switch len(aLabels) {
	case 0:
		filters = append(filters, fmt.Sprintf(
			"anullsrc=channel_layout=stereo:sample_rate=48000,atrim=end=%s[aout]", f(totalS)))
	case 1:
		filters = append(filters, fmt.Sprintf(
			"%sapad=whole_dur=%s,atrim=end=%s[aout]", aLabels[0], f(totalS), f(totalS)))
	default:
		filters = append(filters, fmt.Sprintf(
			"%samix=inputs=%d:duration=longest:normalize=0,apad=whole_dur=%s,atrim=end=%s[aout]",
			strings.Join(aLabels, ""), len(aLabels), f(totalS), f(totalS)))
	}

	args := append([]string{"-hide_banner", "-nostdin", "-v", "error"}, inputs...)
	args = append(args,
		"-filter_complex", strings.Join(filters, ";"),
		"-map", "[vout]", "-map", "[aout]",
		"-c:v", "libx264", "-preset", p.X264, "-crf", p.CRF,
		"-c:a", "aac", "-b:a", p.AudioKbs,
		"-movflags", "+faststart",
		"-y", outPath)
	return args, nil
}
