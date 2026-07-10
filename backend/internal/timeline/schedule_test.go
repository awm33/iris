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

func TestFlattenDissolveWindows(t *testing.T) {
	tr := func(d float64) *TransitionDef { return &TransitionDef{Duration: &d} }
	st := Reduce([]*Op{
		{OpID: "t1", Type: "add_track", Track: &TrackDef{ID: "v1", Kind: "video"}},
		{OpID: "c1", Type: "add_clip", TrackID: "v1", Clip: &ClipDef{ID: "a", Name: "a", VersionID: "va", Start: 0, Duration: 4, Transition: tr(1)}},
		{OpID: "c2", Type: "add_clip", TrackID: "v1", Clip: &ClipDef{ID: "b", Name: "b", VersionID: "vb", Start: 4, Duration: 3}},
		// c ends into a gap → fades to black over the gap piece
		{OpID: "c3", Type: "add_clip", TrackID: "v1", Clip: &ClipDef{ID: "c", Name: "c", VersionID: "vc", Start: 8, Duration: 2, Transition: tr(0.5), InPoint: fl(1)}},
		{OpID: "c4", Type: "add_clip", TrackID: "v1", Clip: &ClipDef{ID: "d", Name: "d", VersionID: "vd", Start: 12, Duration: 2}},
	})
	pieces := Flatten(st, 14)
	type want struct {
		start, dur float64
		clip, from string
		fromSeek   float64
	}
	wants := []want{
		{0, 4, "a", "", 0},
		{4, 1, "b", "a", 4},   // dissolve a→b, FromSeekS = a.InPoint(0)+4
		{5, 2, "b", "", 0},    // rest of b
		{7, 1, "", "", 0},     // gap
		{8, 2, "c", "", 0},    // c plays fully…
		{10, 0.5, "", "c", 3}, // …then fades to black; FromSeekS = c.InPoint(1) + dur(2) = 3
		{10.5, 1.5, "", "", 0},
		{12, 2, "d", "", 0},
	}
	if len(pieces) != len(wants) {
		t.Fatalf("got %d pieces want %d: %+v", len(pieces), len(wants), pieces)
	}
	for i, w := range wants {
		p := pieces[i]
		clip, from, seek := "", "", 0.0
		if p.Clip != nil {
			clip = p.Clip.ID
		}
		if p.BlendFrom != nil {
			from, seek = p.BlendFrom.ID, p.FromSeekS
		}
		if p.Start != w.start || p.Duration != w.dur || clip != w.clip || from != w.from || seek != w.fromSeek {
			t.Errorf("piece %d: got (%.2f %.2f clip=%q from=%q seek=%.2f), want %+v", i, p.Start, p.Duration, clip, from, seek, w)
		}
	}
	// FromSeekS spot checks: a has inPoint 0 dur 4 → 4; c has inPoint 1 dur 2 → 3.
	if pieces[1].FromSeekS != 4 || pieces[5].FromSeekS != 3 {
		t.Errorf("FromSeekS wrong: %v / %v", pieces[1].FromSeekS, pieces[5].FromSeekS)
	}
	// The incoming piece inside the window keeps ITS OWN content timing.
	if pieces[1].Clip.InPoint != 0 || pieces[2].Clip.InPoint != 1 {
		t.Errorf("incoming in-points wrong: %v / %v", pieces[1].Clip.InPoint, pieces[2].Clip.InPoint)
	}
}

// M1 regression (PR37 review): a dissolve never blends into ANOTHER
// track's content — a cross-track winner after the cut is a hard cut on
// both ends, and an overlapping same-track neighbor disqualifies entirely.
func TestFlattenDissolveSameTrackOnly(t *testing.T) {
	tr := func(d float64) *TransitionDef { return &TransitionDef{Duration: &d} }
	st := Reduce([]*Op{
		{OpID: "t1", Type: "add_track", Track: &TrackDef{ID: "v1", Kind: "video"}},
		{OpID: "t2", Type: "add_track", Track: &TrackDef{ID: "v2", Kind: "video"}},
		// a (v1) has a dissolve and ends at 4; v1 is EMPTY after — but a
		// v2 clip starts exactly at 4 and wins the flatten there.
		{OpID: "c1", Type: "add_clip", TrackID: "v1", Clip: &ClipDef{ID: "a", Name: "a", VersionID: "va", Start: 0, Duration: 4, Transition: tr(1)}},
		{OpID: "c2", Type: "add_clip", TrackID: "v2", Clip: &ClipDef{ID: "x", Name: "x", VersionID: "vx", Start: 4, Duration: 3}},
	})
	for _, p := range Flatten(st, 7) {
		if p.BlendFrom != nil {
			t.Fatalf("cross-track dissolve must be a hard cut, got blend piece %+v", p)
		}
	}
	// Overlapping same-track neighbor: no coherent boundary, no blend.
	st2 := Reduce([]*Op{
		{OpID: "t1", Type: "add_track", Track: &TrackDef{ID: "v1", Kind: "video"}},
		{OpID: "c1", Type: "add_clip", TrackID: "v1", Clip: &ClipDef{ID: "a", Name: "a", VersionID: "va", Start: 0, Duration: 4, Transition: tr(1)}},
		{OpID: "c2", Type: "add_clip", TrackID: "v1", Clip: &ClipDef{ID: "b", Name: "b", VersionID: "vb", Start: 3, Duration: 3}},
	})
	for _, p := range Flatten(st2, 6) {
		if p.BlendFrom != nil {
			t.Fatalf("overlapping neighbor must not dissolve, got %+v", p)
		}
	}
}
