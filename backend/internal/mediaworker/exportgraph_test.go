package mediaworker

import (
	"strings"
	"testing"

	"github.com/awm33/iris/backend/internal/timeline"
)

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
		"v1": {Path: "/t/a.mp4", ContentType: "video/mp4", HasAudio: true},
		"v2": {Path: "/t/b.png", ContentType: "image/png"},
		"v3": {Path: "/t/c.mp3", ContentType: "audio/mpeg", HasAudio: true},
		"v4": {Path: "/t/d.mp4", ContentType: "video/mp4", HasAudio: false},
	}

	args, err := buildExportArgs(pieces, entries, versionByClip, sources,
		exportPresets["draft"], 24, 6, "/t/out.mp4")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")

	var graph string
	for i, a := range args {
		if a == "-filter_complex" {
			graph = args[i+1]
		}
	}
	for _, want := range []string{
		// video: source piece trims from its in-point and freeze-pads
		"-ss 1.500000 -t 2.000000 -i /t/a.mp4",
		"tpad=stop_mode=clone:stop_duration=2.000000",
		// still loops for its duration
		"-loop 1 -t 3.000000 -i /t/b.png",
		// audio: embedded (offset 1.5) + music (offset 0.25), silent source dropped
		"-ss 1.500000 -t 2.000000 -i /t/a.mp4 -ss 0.250000 -t 4.000000 -i /t/c.mp3",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q\nargs: %s", want, joined)
		}
	}
	for _, want := range []string{
		"color=black:s=1280x720:r=24:d=1.000000",          // gap piece
		"[v0][v1][v2]concat=n=3:v=1:a=0[vout]",            // 3 pieces in order
		"amix=inputs=2:duration=longest:normalize=0",      // 2 audible entries
		"adelay=1000:all=1",                               // music delayed to 1s
		"apad=whole_dur=6.000000,atrim=end=6.000000[aout]", // padded to total
	} {
		if !strings.Contains(graph, want) {
			t.Errorf("graph missing %q\ngraph: %s", want, graph)
		}
	}
	if strings.Contains(joined, "/t/d.mp4") {
		t.Error("silent source should not be an input")
	}
	if got := strings.Count(joined, "-i "); got != 4 {
		t.Errorf("want 4 inputs (video, still, embedded audio, music), got %d", got)
	}
}

func TestBuildExportArgsSilenceTrack(t *testing.T) {
	clip := &timeline.Clip{ID: "c", Name: "x", VersionID: "v1"}
	args, err := buildExportArgs(
		[]timeline.Piece{{Start: 0, Duration: 2, Clip: clip}},
		nil, map[string]string{"c": "v1"},
		map[string]*exportSource{"v1": {Path: "/t/a.mp4", ContentType: "video/mp4"}},
		exportPresets["master"], 24, 2, "/t/out.mp4")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "anullsrc=channel_layout=stereo:sample_rate=48000,atrim=end=2.000000[aout]") {
		t.Errorf("no-audio timeline must still get a silence track: %s", joined)
	}
}
