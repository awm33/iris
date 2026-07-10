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
		exportPresets["draft"], 24, 6, "/t/out.mp4")
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
		exportPresets["draft"], 24, 3.8, "/t/out.mp4")
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
		exportPresets["draft"], 24, 2, "/t/out.mp4")
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
		exportPresets["draft"], 24, 4, "/t/out.mp4")
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
		exportPresets["master"], 24, 2, "/t/out.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if graph := graphOf(t, args); !strings.Contains(graph, "anullsrc=channel_layout=stereo:sample_rate=48000,atrim=end=2.000000[aout]") {
		t.Errorf("no-audio timeline must still get a silence track: %s", graph)
	}
}
