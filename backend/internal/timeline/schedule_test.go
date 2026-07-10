package timeline

import "testing"

func fl(v float64) *float64 { return &v }

func TestFlattenTopmostWinsAndMerges(t *testing.T) {
	// V-top covers [2,4) over V-bottom's [0,6); expect bottom / top / bottom
	// pieces with bottom's in-point advanced, and gaps at the edges.
	st := Reduce([]*Op{
		{OpID: "t1", Type: "add_track", Track: &TrackDef{ID: "vt", Kind: "video"}},
		{OpID: "t2", Type: "add_track", Track: &TrackDef{ID: "vb", Kind: "video"}},
		{OpID: "c1", Type: "add_clip", TrackID: "vb", Clip: &ClipDef{ID: "b", Name: "b", VersionID: "vv_b", Start: 1, Duration: 6, InPoint: fl(0.5)}},
		{OpID: "c2", Type: "add_clip", TrackID: "vt", Clip: &ClipDef{ID: "a", Name: "a", VersionID: "vv_a", Start: 2, Duration: 2}},
	})
	pieces := Flatten(st, 8)

	type want struct {
		start, dur float64
		clip       string
		inPoint    float64
	}
	wants := []want{
		{0, 1, "", 0},
		{1, 1, "b", 0.5},
		{2, 2, "a", 0},
		{4, 3, "b", 3.5}, // 0.5 + (4-1)
		{7, 1, "", 0},
	}
	if len(pieces) != len(wants) {
		t.Fatalf("got %d pieces, want %d: %+v", len(pieces), len(wants), pieces)
	}
	for i, w := range wants {
		p := pieces[i]
		id := ""
		inp := 0.0
		if p.Clip != nil {
			id, inp = p.Clip.ID, p.Clip.InPoint
		}
		if p.Start != w.start || p.Duration != w.dur || id != w.clip || inp != w.inPoint {
			t.Errorf("piece %d: got (start=%v dur=%v clip=%q in=%v), want %+v", i, p.Start, p.Duration, id, inp, w)
		}
	}
}

func TestFlattenMergesContiguousSameClip(t *testing.T) {
	// A boundary from an AUDIO clip must not split a video piece: flatten
	// only considers video tracks, and same-clip neighbors merge.
	st := Reduce([]*Op{
		{OpID: "t1", Type: "add_track", Track: &TrackDef{ID: "v1", Kind: "video"}},
		{OpID: "t2", Type: "add_track", Track: &TrackDef{ID: "a1", Kind: "audio"}},
		{OpID: "c1", Type: "add_clip", TrackID: "v1", Clip: &ClipDef{ID: "x", Name: "x", VersionID: "vv", Start: 0, Duration: 4}},
		{OpID: "c2", Type: "add_clip", TrackID: "a1", Clip: &ClipDef{ID: "m", Name: "m", VersionID: "vm", Start: 2, Duration: 5}},
	})
	pieces := Flatten(st, 4)
	if len(pieces) != 1 || pieces[0].Clip == nil || pieces[0].Clip.ID != "x" || pieces[0].Duration != 4 {
		t.Fatalf("want one 4s piece of x, got %+v", pieces)
	}
}

func TestAudioEntriesTrimsToTotal(t *testing.T) {
	st := Reduce([]*Op{
		{OpID: "t1", Type: "add_track", Track: &TrackDef{ID: "v1", Kind: "video"}},
		{OpID: "t2", Type: "add_track", Track: &TrackDef{ID: "a1", Kind: "audio"}},
		{OpID: "c1", Type: "add_clip", TrackID: "v1", Clip: &ClipDef{ID: "x", Name: "x", VersionID: "vv", Start: 0, Duration: 3}},
		{OpID: "c2", Type: "add_clip", TrackID: "a1", Clip: &ClipDef{ID: "m", Name: "m", VersionID: "vm", Start: 1, Duration: 9}},
	})
	entries := AudioEntries(st, 3)
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %+v", entries)
	}
	if entries[1].Dur != 2 { // trimmed to totalS
		t.Errorf("audio entry past total not trimmed: %+v", entries[1])
	}
	if entries[0].Kind != "video" || entries[1].Kind != "audio" {
		t.Errorf("kinds wrong: %+v", entries)
	}
}
