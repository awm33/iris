package mediaworker

import "testing"

func TestParseProbeVideo(t *testing.T) {
	raw := []byte(`{
		"streams": [
			{"codec_type": "audio", "r_frame_rate": "0/0"},
			{"codec_type": "video", "width": 640, "height": 360, "r_frame_rate": "24/1"}
		],
		"format": {"duration": "2.000000"}
	}`)
	info, err := parseProbe(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasVideo || info.Width != 640 || info.Height != 360 {
		t.Errorf("video dims wrong: %+v", info)
	}
	if info.FPS != 24 {
		t.Errorf("fps: want 24, got %v", info.FPS)
	}
	if info.DurationS != 2 {
		t.Errorf("duration: want 2, got %v", info.DurationS)
	}
}

func TestParseProbeAudioOnly(t *testing.T) {
	raw := []byte(`{
		"streams": [{"codec_type": "audio", "r_frame_rate": "0/0"}],
		"format": {"duration": "31.5"}
	}`)
	info, err := parseProbe(raw)
	if err != nil {
		t.Fatal(err)
	}
	if info.HasVideo {
		t.Error("audio-only reported video")
	}
	if info.DurationS != 31.5 {
		t.Errorf("duration: want 31.5, got %v", info.DurationS)
	}
}

func TestParseProbeGarbage(t *testing.T) {
	if _, err := parseProbe([]byte(`{"streams":[],"format":{}}`)); err == nil {
		t.Error("want error for probe with no duration and no video stream")
	}
}

func TestParseFrameRate(t *testing.T) {
	cases := map[string]float64{
		"24/1":      24,
		"30000/1001": 29.97002997002997,
		"0/0":       0,
		"":          0,
		"25":        25,
	}
	for in, want := range cases {
		if got := parseFrameRate(in); got != want {
			t.Errorf("parseFrameRate(%q) = %v, want %v", in, got, want)
		}
	}
}
