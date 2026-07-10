package timeline

import (
	"math"
	"sort"
)

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
// (renders black + silence). A non-nil BlendFrom makes it a DISSOLVE
// window: BlendFrom (the outgoing clip, continuing past its out point —
// source material when handles exist, freeze when they don't) fades out
// over Clip's content (nil Clip = fade to black); BlendFrom's source time
// at window time t is FromSeekS + (t - Start).
type Piece struct {
	Start, Duration float64
	Clip            *Clip // nil = gap; Clip.InPoint already offset for mid-clip pieces
	BlendFrom       *Clip
	FromSeekS       float64
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
	return applyTransitions(clips, pieces, totalS)
}

// applyTransitions carves dissolve windows out of the piece list. A
// transition applies only where BOTH sides are actually visible: the
// outgoing clip's tail piece must end at the clip's own end (not cut short
// by a higher track), and the window is taken from whatever piece follows
// (the incoming clip, or a gap = fade to black). The window clamps to that
// piece — a shorter incoming piece shortens the dissolve, and the timeline
// duration never changes. Chained overlaps resolve first-wins (a piece
// already carrying a blend is skipped).
func applyTransitions(clips []*Clip, pieces []Piece, totalS float64) []Piece {
	for _, c := range clips {
		if c.Transition == nil {
			continue
		}
		cut := c.Start + c.Duration
		if cut >= totalS-boundaryEps {
			continue // ends the timeline — no room to fade into
		}
		// The outgoing clip must be the visible winner right up to its end.
		tail := -1
		for i, p := range pieces {
			if p.Clip != nil && p.BlendFrom == nil && p.Clip.ID == c.ID &&
				math.Abs((p.Start+p.Duration)-cut) < boundaryEps {
				tail = i
				break
			}
		}
		if tail == -1 || tail+1 >= len(pieces) {
			continue
		}
		next := pieces[tail+1]
		if next.BlendFrom != nil || math.Abs(next.Start-cut) > boundaryEps {
			continue
		}
		window := math.Min(c.Transition.Duration, next.Duration)
		if window < boundaryEps {
			continue
		}
		from := *c // the ORIGINAL clip: FromSeekS derives from its own extent
		blend := Piece{
			Start:     cut,
			Duration:  window,
			BlendFrom: &from,
			FromSeekS: c.InPoint + c.Duration,
		}
		if next.Clip != nil {
			in := *next.Clip
			in.Start = cut
			in.Duration = window
			blend.Clip = &in
		}
		rest := next
		rest.Start += window
		rest.Duration -= window
		if rest.Clip != nil {
			rc := *rest.Clip
			rc.InPoint += window
			rc.Start += window
			rc.Duration -= window
			rest.Clip = &rc
		}
		if rest.Duration < boundaryEps {
			pieces = append(pieces[:tail+1], append([]Piece{blend}, pieces[tail+2:]...)...)
		} else {
			pieces = append(pieces[:tail+1], append([]Piece{blend, rest}, pieces[tail+2:]...)...)
		}
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
