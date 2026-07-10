package mediaworker

import (
	"strings"
	"testing"

	"github.com/awm33/iris/backend/internal/timeline"
)

func graphOf(t *testing.T, args []string) string {
	t.Helper()
	for i, a := range args {
		if a == "-filter_complex" {
			return args[i+1]
		}
	}
	t.Fatal("no -filter_complex in args")
	return ""
}

// inputPaths returns the -i targets in order — filter [N:v]/[N:a] indices
// must line up with THIS ordering (the classic drift bug).
func inputPaths(args []string) []string {
	var out []string
	for i, a := range args {
		if a == "-i" {
			out = append(out, args[i+1])
		}
	}
	return out
}

func TestBuildExportArgs(t *testing.T) {
	clipVid := &timeline.Clip{ID: "cv", Name: "vid", VersionID: "v1", InPoint: 1.5}
	clipImg := &timeline.Clip{ID: "ci", Name: "img", VersionID: "v2"}
	clipMus := &timeline.Clip{ID: "cm", Name: "music", VersionID: "v3", InPoint: 0.25}
	clipSil := &timeline.Clip{ID: "cs", Name: "silent", VersionID: "v4"}

	pieces := []timeline.Piece{
		{Start: 0, Duration: 2, Clip: clipVid},
		{Start: 2, Duration: 1, Clip: nil}, // gap
		{Start: 3, Duration: 3, Clip: clipImg},
	}
	entries := []timeline.AudioEntry{
		{Clip: clipVid, Kind: "video", Start: 0, Dur: 2},
		{Clip: clipMus, Kind: "audio", Start: 1, Dur: 4},
		{Clip: clipSil, Kind: "video", Start: 3, Dur: 1}, // no audio stream — dropped
	}
	versionByClip := map[string]string{"cv": "v1", "ci": "v2", "cm": "v3", "cs": "v4"}
	sources := map[string]*exportSource{
		"v1": {Path: "/t/a.mp4", ContentType: "video/mp4", HasAudio: true, DurationS: 30},
		"v2": {Path: "/t/b.png", ContentType: "image/png"},
		"v3": {Path: "/t/c.mp3", ContentType: "audio/mpeg", HasAudio: true, DurationS: 60},
		"v4": {Path: "/t/d.mp4", ContentType: "video/mp4", HasAudio: false, DurationS: 30},
	}

	args, err := buildExportArgs(pieces, entries, versionByClip, sources,
		exportPresets["draft"], 24, 6, nil, "/t/out.mp4")
	if err != nil {
		t.Fatal(err)
	}
	graph := graphOf(t, args)

	// Input order IS the filter index space: pin both sides exactly.
	wantInputs := []string{"/t/a.mp4", "/t/b.png", "/t/a.mp4", "/t/c.mp3"}
	gotInputs := inputPaths(args)
	if strings.Join(gotInputs, ",") != strings.Join(wantInputs, ",") {
		t.Fatalf("input order: got %v want %v", gotInputs, wantInputs)
	}
	for _, want := range []string{
		// [0] video piece: seeks its in-point, freeze-pads, cuts on the frame grid
		"[0:v]fps=24,scale=1280:720",
		"tpad=stop_mode=clone:stop_duration=2.000000,trim=end_frame=48,setpts=PTS-STARTPTS[v0]",
		// gap: no input, one frame of margin, exact frame cut
		"color=black:s=1280x720:r=24:d=1.041667,format=yuv420p,trim=end_frame=24,setpts=PTS-STARTPTS[v1]",
		// [1] still holds for 72 frames
		"[1:v]fps=24",
		"trim=end_frame=72,setpts=PTS-STARTPTS[v2]",
		"[v0][v1][v2]concat=n=3:v=1:a=0[vout]",
		// [2] embedded audio at t=0, [3] music delayed one second
		"[2:a]aresample=48000",
		"adelay=0:all=1[a0]",
		"[3:a]aresample=48000",
		"adelay=1000:all=1[a1]",
		"[a0][a1]amix=inputs=2:duration=longest:normalize=0,apad=whole_dur=6.000000,atrim=end=6.000000[aout]",
	} {
		if !strings.Contains(graph, want) {
			t.Errorf("graph missing %q\ngraph: %s", want, graph)
		}
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-ss 1.500000 -t 2.000000 -i /t/a.mp4",
		"-loop 1 -t 3.000000 -i /t/b.png",
		"-ss 0.250000 -t 4.000000 -i /t/c.mp3",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q\nargs: %s", want, joined)
		}
	}
}

// H1 regression: non-frame-aligned cuts must quantize to the output frame
// grid CUMULATIVELY — per-piece seconds-based cuts render ceil(D·fps) frames
// each and the error grows with every cut.
func TestBuildExportArgsFrameGridQuantization(t *testing.T) {
	c1 := &timeline.Clip{ID: "a", Name: "a", VersionID: "v1"}
	c2 := &timeline.Clip{ID: "b", Name: "b", VersionID: "v1", InPoint: 1.9}
	pieces := []timeline.Piece{
		{Start: 0, Duration: 1.9, Clip: c1},
		{Start: 1.9, Duration: 1.9, Clip: c2},
	}
	args, err := buildExportArgs(pieces, nil,
		map[string]string{"a": "v1", "b": "v1"},
		map[string]*exportSource{"v1": {Path: "/t/a.mp4", ContentType: "video/mp4", DurationS: 30}},
		exportPresets["draft"], 24, 3.8, nil, "/t/out.mp4")
	if err != nil {
		t.Fatal(err)
	}
	graph := graphOf(t, args)
	// round(1.9·24)=46, then round(3.8·24)−46=45: Σ=91=round(3.8·24), not 46+46.
	if !strings.Contains(graph, "trim=end_frame=46") || !strings.Contains(graph, "trim=end_frame=45") {
		t.Errorf("cuts not quantized cumulatively to the frame grid: %s", graph)
	}
}

// H2 regression: an in-point at/past source EOF decodes zero frames and the
// piece silently vanishes — the seek must clamp to one frame before EOF so
// tpad has a frame to freeze (the preview's behavior).
func TestBuildExportArgsClampsSeekToSourceEOF(t *testing.T) {
	c := &timeline.Clip{ID: "a", Name: "a", VersionID: "v1", InPoint: 7}
	args, err := buildExportArgs(
		[]timeline.Piece{{Start: 0, Duration: 2, Clip: c}}, nil,
		map[string]string{"a": "v1"},
		map[string]*exportSource{"v1": {Path: "/t/a.mp4", ContentType: "video/mp4", DurationS: 5}},
		exportPresets["draft"], 24, 2, nil, "/t/out.mp4")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-ss 4.958333 -t 2.000000 -i /t/a.mp4") {
		t.Errorf("seek past EOF not clamped to duration − 1 frame: %s", joined)
	}
}

// Sub-half-frame slivers quantize to zero frames and must be skipped —
// a zero-frame trim gives concat an empty stream.
func TestBuildExportArgsSkipsZeroFramePieces(t *testing.T) {
	c := &timeline.Clip{ID: "a", Name: "a", VersionID: "v1"}
	pieces := []timeline.Piece{
		{Start: 0, Duration: 2, Clip: c},
		{Start: 2, Duration: 0.01, Clip: nil}, // rounds to 0 frames
		{Start: 2.01, Duration: 1.99, Clip: c},
	}
	args, err := buildExportArgs(pieces, nil,
		map[string]string{"a": "v1"},
		map[string]*exportSource{"v1": {Path: "/t/a.mp4", ContentType: "video/mp4", DurationS: 30}},
		exportPresets["draft"], 24, 4, nil, "/t/out.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if graph := graphOf(t, args); !strings.Contains(graph, "concat=n=2:") {
		t.Errorf("zero-frame piece not skipped: %s", graph)
	}
}

func TestBuildExportArgsSilenceTrack(t *testing.T) {
	clip := &timeline.Clip{ID: "c", Name: "x", VersionID: "v1"}
	args, err := buildExportArgs(
		[]timeline.Piece{{Start: 0, Duration: 2, Clip: clip}},
		nil, map[string]string{"c": "v1"},
		map[string]*exportSource{"v1": {Path: "/t/a.mp4", ContentType: "video/mp4", DurationS: 30}},
		exportPresets["master"], 24, 2, nil, "/t/out.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if graph := graphOf(t, args); !strings.Contains(graph, "anullsrc=channel_layout=stereo:sample_rate=48000,atrim=end=2.000000[aout]") {
		t.Errorf("no-audio timeline must still get a silence track: %s", graph)
	}
}

func TestBuildExportArgsCaptionBurnIn(t *testing.T) {
	clip := &timeline.Clip{ID: "c", Name: "x", VersionID: "v1"}
	caps := []captionOverlay{
		{Path: "/tmp/x/cap0.png", Start: 0, End: 2.5},
		{Path: "/tmp/x/cap1.png", Start: 2.5, End: 4},
	}
	args, err := buildExportArgs(
		[]timeline.Piece{{Start: 0, Duration: 4, Clip: clip}},
		nil, map[string]string{"c": "v1"},
		map[string]*exportSource{"v1": {Path: "/t/a.mp4", ContentType: "video/mp4", DurationS: 30}},
		exportPresets["draft"], 24, 4, caps, "/t/out.mp4")
	if err != nil {
		t.Fatal(err)
	}
	graph := graphOf(t, args)
	// Video is input 0; caption PNGs are inputs 1 and 2, chained after
	// concat, last link labeled [vout].
	for _, want := range []string{
		"concat=n=1:v=1:a=0[vcat]",
		"[vcat][1:v]overlay=(main_w-overlay_w)/2:main_h-overlay_h-36:enable='gte(t,0.000000)*lt(t,2.500000)'[vcap0]",
		"[vcap0][2:v]overlay=(main_w-overlay_w)/2:main_h-overlay_h-36:enable='gte(t,2.500000)*lt(t,4.000000)'[vout]",
	} {
		if !strings.Contains(graph, want) {
			t.Errorf("graph missing %q\ngraph: %s", want, graph)
		}
	}
	if got := inputPaths(args); strings.Join(got, ",") != "/t/a.mp4,/tmp/x/cap0.png,/tmp/x/cap1.png" {
		t.Errorf("input order wrong: %v", got)
	}
	// A path with filter metacharacters is a bug upstream, never escaped through.
	if _, err := buildExportArgs(
		[]timeline.Piece{{Start: 0, Duration: 2, Clip: clip}},
		nil, map[string]string{"c": "v1"},
		map[string]*exportSource{"v1": {Path: "/t/a.mp4", ContentType: "video/mp4", DurationS: 30}},
		exportPresets["draft"], 24, 2,
		[]captionOverlay{{Path: "/tmp/evil':x/cap.png", Start: 0, End: 1}}, "/t/out.mp4"); err == nil {
		t.Error("unsafe caption path accepted into filter graph")
	}
}

func TestRenderCaptionPNG(t *testing.T) {
	img, err := renderCaptionPNG("Hello from the mixing desk — a longer caption that should wrap to more than one line at 1280", 1280)
	if err != nil {
		t.Fatal(err)
	}
	if len(img) == 0 || string(img[1:4]) != "PNG" {
		t.Fatalf("not a png (%d bytes)", len(img))
	}
	if _, err := renderCaptionPNG("   ", 1280); err == nil {
		t.Error("blank caption must error (callers filter blanks)")
	}
}

func TestBuildSRT(t *testing.T) {
	st := &timeline.State{Tracks: []*timeline.Track{
		{ID: "v1", Kind: "video", Clips: []*timeline.Clip{{ID: "x", Text: "ignored — not a caption track"}}},
		{ID: "c1", Kind: "caption", Clips: []*timeline.Clip{
			{ID: "b", Text: "second", Start: 2.5, Duration: 2},
			{ID: "a", Text: " first ", Start: 0, Duration: 2.5},
			{ID: "e", Text: "   ", Start: 5, Duration: 1}, // blank text drops
		}},
	}}
	got := buildSRT(st)
	want := "1\n00:00:00,000 --> 00:00:02,500\nfirst\n\n2\n00:00:02,500 --> 00:00:04,500\nsecond\n\n"
	if got != want {
		t.Errorf("srt mismatch\ngot:  %q\nwant: %q", got, want)
	}
	if buildSRT(&timeline.State{}) != "" {
		t.Error("captionless timeline must produce empty srt")
	}
}

func TestBuildTranscribeArgs(t *testing.T) {
	clip := &timeline.Clip{ID: "c", Name: "x", VersionID: "v1", InPoint: 0.5}
	args, audible := buildTranscribeArgs(
		[]timeline.AudioEntry{{Clip: clip, Kind: "video", Start: 1, Dur: 3}},
		map[string]string{"c": "v1"},
		map[string]*exportSource{"v1": {Path: "/t/a.mp4", ContentType: "video/mp4", HasAudio: true, DurationS: 30}},
		4, "/t/mix.wav")
	if audible != 1 {
		t.Fatalf("audible = %d, want 1", audible)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-ss 0.500000 -t 3.000000 -i /t/a.mp4",
		"adelay=1000:all=1",
		"-map [aout] -ar 16000 -ac 1 -c:a pcm_s16le -y /t/mix.wav",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q\nargs: %s", want, joined)
		}
	}
	// Silent timeline: no engine run.
	_, audible = buildTranscribeArgs(nil, nil, nil, 4, "/t/mix.wav")
	if audible != 0 {
		t.Errorf("audible = %d, want 0", audible)
	}
}

// The full input-index space at once: video pieces (with a no-input gap),
// caption PNGs, then audio entries — the delete-an-increment bug class
// across ALL THREE sections must fail loudly.
func TestBuildExportArgsCombinedInputOrdering(t *testing.T) {
	vClip := &timeline.Clip{ID: "cv", Name: "v", VersionID: "v1"}
	aClip := &timeline.Clip{ID: "ca", Name: "a", VersionID: "v2", InPoint: 0.5}
	pieces := []timeline.Piece{
		{Start: 0, Duration: 2, Clip: vClip},
		{Start: 2, Duration: 1, Clip: nil}, // gap: no input
		{Start: 3, Duration: 1, Clip: vClip},
	}
	captions := []captionOverlay{{Path: "/t/cap0.png", Start: 1, End: 3.5}}
	entries := []timeline.AudioEntry{{Clip: aClip, Kind: "audio", Start: 0, Dur: 4}}
	args, err := buildExportArgs(pieces, entries,
		map[string]string{"cv": "v1", "ca": "v2"},
		map[string]*exportSource{
			"v1": {Path: "/t/a.mp4", ContentType: "video/mp4", DurationS: 30},
			"v2": {Path: "/t/m.mp3", ContentType: "audio/mpeg", HasAudio: true, DurationS: 60},
		},
		exportPresets["draft"], 24, 4, captions, "/t/out.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(inputPaths(args), ","); got != "/t/a.mp4,/t/a.mp4,/t/cap0.png,/t/m.mp3" {
		t.Fatalf("input order: %s", got)
	}
	graph := graphOf(t, args)
	for _, want := range []string{
		"[0:v]", "[1:v]", // two video piece inputs (gap consumes none)
		"[vcat][2:v]overlay", // caption PNG is input 2
		"[3:a]aresample",     // audio is input 3
	} {
		if !strings.Contains(graph, want) {
			t.Errorf("graph missing %q\ngraph: %s", want, graph)
		}
	}
}

// Color grades render as one numeric lutrgb between scale and pad (the
// preview never colors the letterbox), stills stay ungraded in v1, and a
// neutral grade adds nothing.
func TestBuildExportArgsColorGrade(t *testing.T) {
	graded := &timeline.Clip{ID: "g", Name: "g", VersionID: "v1",
		Color: &timeline.Color{Exposure: 1, Contrast: 0.8, Temp: 0.5}}
	neutral := &timeline.Clip{ID: "n", Name: "n", VersionID: "v1",
		Color: &timeline.Color{Exposure: 0, Contrast: 1, Temp: 0}}
	args, err := buildExportArgs(
		[]timeline.Piece{
			{Start: 0, Duration: 1, Clip: graded},
			{Start: 1, Duration: 1, Clip: neutral},
		},
		nil, map[string]string{"g": "v1", "n": "v1"},
		map[string]*exportSource{"v1": {Path: "/t/a.mp4", ContentType: "video/mp4", DurationS: 30}},
		exportPresets["draft"], 24, 2, nil, "/t/out.mp4")
	if err != nil {
		t.Fatal(err)
	}
	graph := graphOf(t, args)
	// exposure 1 → ×2; contrast 0.8 around 127.5; warm 0.5 → g×0.95 b×0.85.
	// format=gbrp forces 8-bit before the 0..255 expressions (10-bit
	// sources negotiate deep GBRP and would crush); +0.5 rounds like the
	// canvas instead of truncating.
	want := "force_original_aspect_ratio=decrease," +
		"format=gbrp,lutrgb=r='clip((clip(val*2.000000\\,0\\,255)-127.5)*0.800000+127.5\\,0\\,255)*1.000000+0.5'" +
		":g='clip((clip(val*2.000000\\,0\\,255)-127.5)*0.800000+127.5\\,0\\,255)*0.950000+0.5'" +
		":b='clip((clip(val*2.000000\\,0\\,255)-127.5)*0.800000+127.5\\,0\\,255)*0.850000+0.5',pad="
	if !strings.Contains(graph, want) {
		t.Errorf("graded piece missing format=gbrp,lutrgb between scale and pad\ngraph: %s", graph)
	}
	if strings.Count(graph, "lutrgb") != 1 {
		t.Errorf("neutral grade must add no filter (want exactly 1 lutrgb): %s", graph)
	}
}

// Cool temps attenuate red/green; stills stay ungraded even when their
// clip carries a grade (the preview's <img> overlay doesn't color in v1).
func TestBuildExportArgsColorCoolAndStill(t *testing.T) {
	cool := &timeline.Clip{ID: "c", Name: "c", VersionID: "v1",
		Color: &timeline.Color{Exposure: 0, Contrast: 1, Temp: -1}}
	still := &timeline.Clip{ID: "s", Name: "s", VersionID: "v2",
		Color: &timeline.Color{Exposure: 2, Contrast: 2, Temp: 1}}
	args, err := buildExportArgs(
		[]timeline.Piece{
			{Start: 0, Duration: 1, Clip: cool},
			{Start: 1, Duration: 1, Clip: still},
		},
		nil, map[string]string{"c": "v1", "s": "v2"},
		map[string]*exportSource{
			"v1": {Path: "/t/a.mp4", ContentType: "video/mp4", DurationS: 30},
			"v2": {Path: "/t/b.png", ContentType: "image/png"},
		},
		exportPresets["draft"], 24, 2, nil, "/t/out.mp4")
	if err != nil {
		t.Fatal(err)
	}
	graph := graphOf(t, args)
	if !strings.Contains(graph, "*0.700000+0.5':g='") || !strings.Contains(graph, "*0.900000+0.5':b='") {
		t.Errorf("cool temp gains wrong (want r×0.70, g×0.90): %s", graph)
	}
	if strings.Count(graph, "lutrgb") != 1 {
		t.Errorf("still must stay ungraded (want exactly 1 lutrgb): %s", graph)
	}
}
