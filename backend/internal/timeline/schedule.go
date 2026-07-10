package timeline

import "sort"

// Export flattening. The client compositor picks the FIRST segment covering
// t with segments in track-priority order (schedule.ts segmentAt) — the
// export must paint the same pixels, so Flatten produces the piecewise
// topmost-wins timeline: contiguous pieces, each either a clip span or a
// gap, covering [0, Duration).
//
// Audio parity (TimelinePage audioSegments + AudioMixer): EVERY video-track
// clip with a source contributes its embedded audio — including clips
// hidden under a higher track — and every audio-track clip plays; overlaps
// mix. So audio is per-clip, not per-flattened-piece.

// Piece is one span of the flattened video timeline. A nil Clip is a gap
// (renders black + silence).
type Piece struct {
	Start, Duration float64
	Clip            *Clip // nil = gap; Clip.InPoint already offset for mid-clip pieces
}

const boundaryEps = 1e-6

// Flatten resolves video tracks (in track order = priority) to contiguous
// pieces over [0, totalS). Clips whose span is fully covered by higher
// tracks contribute nothing; partial covers split into pieces with the
// in-point advanced so content stays anchored.
func Flatten(st *State, totalS float64) []Piece {
	var clips []*Clip
	for _, t := range st.Tracks {
		if t.Kind != "video" {
			continue
		}
		clips = append(clips, t.Clips...)
	}

	// Boundary set: every clip edge inside [0, totalS), plus the ends.
	bounds := []float64{0, totalS}
	for _, c := range clips {
		for _, b := range []float64{c.Start, c.Start + c.Duration} {
			if b > 0 && b < totalS {
				bounds = append(bounds, b)
			}
		}
	}
	sort.Float64s(bounds)
	// Dedup within epsilon (starts are r2-rounded upstream, but blade/ripple
	// arithmetic can leave float dust).
	uniq := bounds[:1]
	for _, b := range bounds[1:] {
		if b-uniq[len(uniq)-1] > boundaryEps {
			uniq = append(uniq, b)
		}
	}

	var pieces []Piece
	for i := 0; i+1 < len(uniq); i++ {
		t0, t1 := uniq[i], uniq[i+1]
		mid := (t0 + t1) / 2
		// First covering clip in track-priority order wins (segmentAt).
		var winner *Clip
		for _, c := range clips {
			if mid >= c.Start && mid < c.Start+c.Duration {
				winner = c
				break
			}
		}
		p := Piece{Start: t0, Duration: t1 - t0}
		if winner != nil {
			cp := *winner
			cp.InPoint = winner.InPoint + (t0 - winner.Start)
			cp.Start = t0
			cp.Duration = t1 - t0
			p.Clip = &cp
		}
		// Merge with the previous piece when both are gaps or both continue
		// the same clip contiguously — fewer ffmpeg filter inputs, same
		// pixels.
		if n := len(pieces); n > 0 {
			prev := &pieces[n-1]
			sameGap := prev.Clip == nil && p.Clip == nil
			sameClip := prev.Clip != nil && p.Clip != nil && prev.Clip.ID == p.Clip.ID
			if sameGap || sameClip {
				prev.Duration += p.Duration
				continue
			}
		}
		pieces = append(pieces, p)
	}
	return pieces
}

// AudioEntry is one mixed audio source span (offsets in seconds).
type AudioEntry struct {
	Clip  *Clip
	Kind  string // "video" (embedded audio) | "audio" (audio-track clip)
	Start float64
	Dur   float64
}

// AudioEntries lists every audio-bearing clip span, unmixed and unclipped
// by track priority — the mixer semantics ("every unmuted track sounds").
// Entries past totalS are trimmed; the caller drops sources that turn out
// to have no audio stream (the client's null-cached no-audio sources).
func AudioEntries(st *State, totalS float64) []AudioEntry {
	var out []AudioEntry
	for _, t := range st.Tracks {
		for _, c := range t.Clips {
			if c.Start >= totalS {
				continue
			}
			dur := min(c.Duration, totalS-c.Start)
			out = append(out, AudioEntry{Clip: c, Kind: t.Kind, Start: c.Start, Dur: dur})
		}
	}
	return out
}
