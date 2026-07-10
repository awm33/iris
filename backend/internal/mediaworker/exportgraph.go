package mediaworker

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/awm33/iris/backend/internal/timeline"
)

// buildExportArgs assembles the full ffmpeg invocation for an export: one
// input per video piece / audio entry (no split plumbing — input counts are
// dogfood-scale), a filter graph that normalizes every piece to the preset's
// frame (fps/scale/pad), concats video, and mixes audio.
//
// Duration exactness is load-bearing, and seconds-based cuts don't provide
// it: `trim=end=D` keeps ceil(D·fps) frames, so every non-frame-aligned cut
// renders LONG and the error accumulates across concat (review PR34-H1 —
// reproduced at ~20ms/cut, unbounded in cut count). So pieces are cut on
// the OUTPUT FRAME GRID: piece boundaries quantize to round(t·fps) frames,
// every branch overproduces slightly (tpad clone / color margin) and
// `trim=end_frame` cuts exactly — Σframes = round(totalS·fps) by
// construction, and audio delays derive from the same quantized grid.
//
// A clip whose in-point outruns its source must freeze on the last frame
// like the preview does — but `-ss` at/past EOF decodes ZERO frames and
// tpad has nothing to clone: ffmpeg exits 0 with the piece silently missing
// (review PR34-H2). The seek is clamped to one frame before the probed
// source duration so at least one frame always decodes.
// captionOverlay is one Go-rendered caption image with its enable window.
type captionOverlay struct {
	Path       string
	Start, End float64
}

func buildExportArgs(
	pieces []timeline.Piece,
	entries []timeline.AudioEntry,
	versionByClip map[string]string,
	sources map[string]*exportSource,
	p exportPreset,
	fps int,
	totalS float64,
	captions []captionOverlay,
	ducks []timeline.DuckWindow,
	outPath string,
) ([]string, error) {
	if fps <= 0 {
		fps = 24
	}
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', 6, 64) }
	frameAt := func(t float64) int { return int(math.Round(t * float64(fps))) }
	totalF := frameAt(totalS)
	totalQ := float64(totalF) / float64(fps)
	// The color grade slots between scale and pad: the preview colors only
	// the painted FRAME (canvas filter + multiply rect), never the black
	// letterbox — contrast ≠ 1 would lift padding to gray if applied after.
	scaleFor := func(clip *timeline.Clip) string {
		grade := ""
		if clip != nil {
			grade = colorFilter(clip.Color)
		}
		return fmt.Sprintf(
			"fps=%d,scale=%d:%d:force_original_aspect_ratio=decrease%s,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1,format=yuv420p",
			fps, p.W, p.H, grade, p.W, p.H)
	}

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

	// emitVisual renders ONE content stream (video / still / black) cut to
	// exactly `frames`, labeled `out`. ss is the source seek for video
	// content — a blend's outgoing side seeks its OUT point instead of its
	// in point (continuing past the cut; freeze when handles run out).
	emitVisual := func(clip *timeline.Clip, ss float64, dur float64, frames int, out string) {
		src := (*exportSource)(nil)
		if clip != nil {
			src = srcFor(clip)
		}
		switch {
		case src != nil && strings.HasPrefix(src.ContentType, "video/"):
			if src.DurationS > 0 {
				ss = math.Min(ss, math.Max(0, src.DurationS-1.0/float64(fps)))
			}
			inputs = append(inputs, "-ss", f(ss), "-t", f(dur), "-i", src.Path)
			filters = append(filters, fmt.Sprintf(
				"[%d:v]%s,tpad=stop_mode=clone:stop_duration=%s,trim=end_frame=%d,setpts=PTS-STARTPTS[%s]",
				nInput, scaleFor(clip), f(dur), frames, out))
			nInput++
		case src != nil && strings.HasPrefix(src.ContentType, "image/"):
			// Stills (image takes) hold the frame for the piece duration —
			// the preview's overlay semantics. No grade in v1 (the preview
			// overlay doesn't color either).
			inputs = append(inputs, "-loop", "1", "-t", f(dur), "-i", src.Path)
			filters = append(filters, fmt.Sprintf(
				"[%d:v]%s,tpad=stop_mode=clone:stop_duration=%s,trim=end_frame=%d,setpts=PTS-STARTPTS[%s]",
				nInput, scaleFor(nil), f(dur), frames, out))
			nInput++
		default:
			// Gap — or a clip whose source can't paint (audio on a video
			// track can't happen via the UI; render black over failing).
			// One frame of margin on d, trim cuts exact.
			filters = append(filters, fmt.Sprintf(
				"color=black:s=%dx%d:r=%d:d=%s,format=yuv420p,trim=end_frame=%d,setpts=PTS-STARTPTS[%s]",
				p.W, p.H, fps, f(float64(frames+1)/float64(fps)), frames, out))
		}
	}

	// Video pieces → [v0..vN], in timeline order. Sub-half-frame slivers
	// quantize to zero frames and are skipped entirely (a zero-frame trim
	// would make concat fail on an empty stream).
	var vLabels []string
	for i, piece := range pieces {
		startF := frameAt(piece.Start)
		endF := frameAt(piece.Start + piece.Duration)
		frames := endF - startF
		if frames <= 0 {
			continue
		}
		dur := float64(frames) / float64(fps)
		label := fmt.Sprintf("v%d", i)
		if piece.BlendFrom != nil {
			// Dissolve window: outgoing (seeked to its out point) fades
			// into the incoming content (or black). Both sides are already
			// normalized to identical size/fps/format, and both are cut to
			// `frames`, so xfade(offset=0, duration=window) emits exactly
			// the window — the trailing trim is belt and braces.
			emitVisual(piece.BlendFrom, piece.FromSeekS, dur, frames, label+"a")
			var inSS float64
			if piece.Clip != nil {
				inSS = piece.Clip.InPoint
			}
			emitVisual(piece.Clip, inSS, dur, frames, label+"b")
			filters = append(filters, fmt.Sprintf(
				"[%sa][%sb]xfade=transition=fade:duration=%s:offset=0,trim=end_frame=%d,setpts=PTS-STARTPTS[%s]",
				label, label, f(dur), frames, label))
		} else {
			var ss float64
			if piece.Clip != nil {
				ss = piece.Clip.InPoint
			}
			emitVisual(piece.Clip, ss, dur, frames, label)
		}
		vLabels = append(vLabels, "["+label+"]")
	}
	if len(vLabels) == 0 {
		return nil, fmt.Errorf("no video pieces to render")
	}
	// Captions burn in AFTER concat, as Go-rendered PNGs composited with
	// `overlay` + numeric enable windows (see captionimg.go for why not
	// subtitles/drawtext). User text never enters the filter graph.
	concatOut := "[vout]"
	if len(captions) > 0 {
		concatOut = "[vcat]"
	}
	filters = append(filters, fmt.Sprintf("%sconcat=n=%d:v=1:a=0%s",
		strings.Join(vLabels, ""), len(vLabels), concatOut))
	cur := "[vcat]"
	for i, c := range captions {
		if _, err := filterSafePath(c.Path); err != nil {
			return nil, err
		}
		inputs = append(inputs, "-i", c.Path)
		out := fmt.Sprintf("[vcap%d]", i)
		if i == len(captions)-1 {
			out = "[vout]"
		}
		// Half-open window (gte*lt, not between): between() is inclusive on
		// BOTH ends, so abutting captions would double-render on a shared
		// frame-grid boundary — and the preview's clipAt is half-open.
		filters = append(filters, fmt.Sprintf(
			"%s[%d:v]overlay=(main_w-overlay_w)/2:main_h-overlay_h-%d:enable='gte(t,%s)*lt(t,%s)'%s",
			cur, nInput, p.H/20, f(c.Start), f(c.End), out))
		nInput++
		cur = out
	}

	aInputs, aFilters, _ := buildAudioSection(entries, srcFor, fps, totalQ, nInput, ducks)
	inputs = append(inputs, aInputs...)
	filters = append(filters, aFilters...)

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

// buildAudioSection emits the mixer-parity audio graph ending at [aout]:
// one input per audible entry, per-entry pad/trim/delay on the video frame
// grid, then amix (unnormalized — WebAudio sums) padded/cut to exactly
// totalQ. Silence-only timelines still get a track: players expect one, and
// the mix keeps A/V duration equal. An audio in-point past EOF needs no
// clamp: zero decoded samples + apad = silence, which IS the preview's
// behavior for an out-of-range audio offset. Shared by export and
// transcribe so "what sounds" can never diverge between them.
func buildAudioSection(
	entries []timeline.AudioEntry,
	srcFor func(*timeline.Clip) *exportSource,
	fps int,
	totalQ float64,
	nInput int,
	ducks []timeline.DuckWindow,
) (inputs []string, filters []string, audible int) {
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', 6, 64) }
	frameAt := func(t float64) int { return int(math.Round(t * float64(fps))) }
	var aLabels []string
	for _, e := range entries {
		src := srcFor(e.Clip)
		if src == nil || !src.HasAudio {
			continue // stills, captions, silent sources — the mixer's null-cached skip
		}
		label := fmt.Sprintf("a%d", len(aLabels))
		delayMs := int(math.Round(float64(frameAt(e.Start)) / float64(fps) * 1000))
		duck := ""
		if !e.Clip.Speech {
			duck = duckVolume(e, ducks, f)
		}
		inputs = append(inputs,
			"-ss", f(e.Clip.InPoint), "-t", f(e.Dur), "-i", src.Path)
		filters = append(filters, fmt.Sprintf(
			"[%d:a]aresample=48000,aformat=sample_fmts=fltp:channel_layouts=stereo,"+
				"apad=pad_dur=%s,atrim=end=%s,asetpts=PTS-STARTPTS%s,adelay=%d:all=1[%s]",
			nInput, f(e.Dur), f(e.Dur), duck, delayMs, label))
		nInput++
		aLabels = append(aLabels, "["+label+"]")
	}
	switch len(aLabels) {
	case 0:
		filters = append(filters, fmt.Sprintf(
			"anullsrc=channel_layout=stereo:sample_rate=48000,atrim=end=%s[aout]", f(totalQ)))
	case 1:
		filters = append(filters, fmt.Sprintf(
			"%sapad=whole_dur=%s,atrim=end=%s[aout]", aLabels[0], f(totalQ), f(totalQ)))
	default:
		filters = append(filters, fmt.Sprintf(
			"%samix=inputs=%d:duration=longest:normalize=0,apad=whole_dur=%s,atrim=end=%s[aout]",
			strings.Join(aLabels, ""), len(aLabels), f(totalQ), f(totalQ)))
	}
	return inputs, filters, len(aLabels)
}

// duckVolume renders the deterministic duck curve for one NON-speech entry
// as a volume filter link (leading comma; empty when no window overlaps).
// Placed after asetpts, so t is ENTRY-LOCAL: window times shift by −Start.
// The curve is the shared contract (timeline.DuckWindows / duckWindows in
// doc-runtime): g(t) = 1 − (1−DuckLevel)·coverage, coverage = max over
// windows of the DuckRampS-edged trapezoid — numeric-only expression.
func duckVolume(e timeline.AudioEntry, ducks []timeline.DuckWindow, f func(float64) string) string {
	cov := ""
	for _, w := range ducks {
		s0 := w.Start - e.Start
		e0 := w.End - e.Start
		if e0+timeline.DuckRampS <= 0 || s0-timeline.DuckRampS >= e.Dur {
			continue // no overlap with this entry's span (incl. ramps)
		}
		term := fmt.Sprintf("clip(min((t-%s)/%s\\,(%s-t)/%s)\\,0\\,1)",
			f(s0), f(timeline.DuckRampS), f(e0+timeline.DuckRampS), f(timeline.DuckRampS))
		if cov == "" {
			cov = term
		} else {
			cov = fmt.Sprintf("max(%s\\,%s)", cov, term)
		}
	}
	if cov == "" {
		return ""
	}
	return fmt.Sprintf(",volume=volume='1-%s*(%s)':eval=frame", f(1-timeline.DuckLevel), cov)
}

// filterSafePath admits a path into filter_complex only when it contains no
// filter metacharacters — worker temp paths never do; anything else is a
// bug, not an escaping exercise.
func filterSafePath(p string) (string, error) {
	if strings.ContainsAny(p, `':,;[]\`) {
		return "", fmt.Errorf("path %q unsafe for filter graph", p)
	}
	return p, nil
}

// captionClips collects every caption-track clip with text, chronological
// across tracks — the shared source for the SRT sidecar and the burn-in.
func captionClips(st *timeline.State) []*timeline.Clip {
	var caps []*timeline.Clip
	for _, tr := range st.Tracks {
		if tr.Kind != "caption" {
			continue
		}
		for _, c := range tr.Clips {
			if strings.TrimSpace(c.Text) != "" {
				caps = append(caps, c)
			}
		}
	}
	sort.SliceStable(caps, func(i, j int) bool { return caps[i].Start < caps[j].Start })
	return caps
}

// buildSRT renders the caption clips as SubRip. Empty string when no
// caption text exists. Whitespace runs (incl. interior newlines — a blank
// line would terminate the SubRip block early) collapse to single spaces;
// set_clip_text accepts any string from any client.
func buildSRT(st *timeline.State) string {
	var b strings.Builder
	for i, c := range captionClips(st) {
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n",
			i+1, srtTime(c.Start), srtTime(c.Start+c.Duration),
			strings.Join(strings.Fields(c.Text), " "))
	}
	return b.String()
}

func srtTime(t float64) string {
	ms := int(math.Round(t * 1000))
	return fmt.Sprintf("%02d:%02d:%02d,%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}

// colorFilter renders a clip grade as one numeric-only lutrgb link (leading
// comma — empty for neutral). The math is the PREVIEW's, per channel in
// sRGB: clamp after exposure, clamp after contrast, then attenuation-only
// temperature gains — canvas `brightness() contrast()` + a multiply-blend
// rect compute this to within ~1-2 LSB (lutrgb truncates where canvas
// rounds — the trailing +0.5 makes lutrgb round-half-up; the multiply
// fill quantizes its gain to 1/255; the golden-frame slice must budget
// a small tolerance). format=gbrp is LOAD-BEARING: the expressions
// assume 8-bit (0..255, pivot 127.5), but lutrgb negotiates 9..16-bit
// GBRP for deep sources — a 10-bit original (phone HDR) would crush to
// val≤255/1023 (review PR36-H1, reproduced). 8-bit is also exactly what
// the preview canvas computes, so it is the parity-true depth. The +0.5
// cannot overflow: clip ≤255, gain ≤1, max 255.5 → 255.
func colorFilter(c *timeline.Color) string {
	if c == nil || (c.Exposure == 0 && c.Contrast == 1 && c.Temp == 0) {
		return ""
	}
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', 6, 64) }
	e := math.Pow(2, c.Exposure)
	rG, gG, bG := 1.0, 1.0, 1.0
	if c.Temp > 0 { // warm: attenuate blue, a little green
		gG = 1 - 0.1*c.Temp
		bG = 1 - 0.3*c.Temp
	} else if c.Temp < 0 { // cool: attenuate red, a little green
		rG = 1 + 0.3*c.Temp
		gG = 1 + 0.1*c.Temp
	}
	expr := func(gain float64) string {
		return fmt.Sprintf("clip((clip(val*%s\\,0\\,255)-127.5)*%s+127.5\\,0\\,255)*%s+0.5",
			f(e), f(c.Contrast), f(gain))
	}
	return fmt.Sprintf(",format=gbrp,lutrgb=r='%s':g='%s':b='%s'", expr(rG), expr(gG), expr(bG))
}
