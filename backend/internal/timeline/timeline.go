// Package timeline is the Go port of the timeline op-log reducer in
// web/packages/doc-runtime (ops + activeOps + reduceTimeline). The export
// service replays the persisted log server-side, so BOTH reducers read the
// same vocabulary — shared JSON vectors under spec/timeline-vectors guard
// the two against drift (vectors_test.go here, vectors.test.ts there).
// Port faithfully or not at all: a semantic difference between preview and
// export is a rendering bug the golden-frame slice would only catch later.
package timeline

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

// Op mirrors doc-runtime's TimelineOp envelope. Optional numeric fields are
// pointers: trim_clip's absent start and start 0 are different edits.
type Op struct {
	OpID       string         `json:"op_id"`
	Type       string         `json:"type"`
	Track      *TrackDef      `json:"track,omitempty"`
	Index      *int           `json:"index,omitempty"`
	TrackID    string         `json:"track_id,omitempty"`
	Clip       *ClipDef       `json:"clip,omitempty"`
	ClipID     string         `json:"clip_id,omitempty"`
	Start      *float64       `json:"start,omitempty"`
	Duration   *float64       `json:"duration,omitempty"`
	InPoint    *float64       `json:"in_point,omitempty"`
	Text       *string        `json:"text,omitempty"`
	Color      *ColorDef      `json:"color,omitempty"`
	Transition *TransitionDef `json:"transition,omitempty"`
	Speech     *bool          `json:"speech,omitempty"`
	Target     string         `json:"target,omitempty"`
}

// ColorDef is the wire form of a color grade — pointer fields because an
// ABSENT field means that field's neutral (the TS clamp's isFinite guard),
// which a zero-value decode cannot represent (0 is a valid contrast).
type ColorDef struct {
	Exposure *float64 `json:"exposure,omitempty"` // stops, -3..3
	Contrast *float64 `json:"contrast,omitempty"` // 0..2, 1 = neutral
	Temp     *float64 `json:"temp,omitempty"`     // -1 (cool) .. 1 (warm)
}

// UnmarshalJSON is deliberately TOLERANT: the TS reducer treats any
// malformed color (non-object, wrong-typed or null fields) as
// fields-absent-therefore-neutral, and a strict decode here would fail the
// WHOLE log in ParseOps — bricking export and transcribe over one garbage
// op any client can append. Parity demands the same leniency.
func (c *ColorDef) UnmarshalJSON(b []byte) error {
	*c = ColorDef{}
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return nil // non-object → every field absent (TS: clampColor(junk) → neutral)
	}
	get := func(key string) *float64 {
		raw, ok := m[key]
		if !ok {
			return nil
		}
		var v *float64 // pointer target: JSON null must stay absent, not become 0
		if json.Unmarshal(raw, &v) != nil {
			return nil
		}
		return v
	}
	c.Exposure, c.Contrast, c.Temp = get("exposure"), get("contrast"), get("temp")
	return nil
}

// Color is the reduced (clamped, defaulted) grade. Neutral = {0, 1, 0}.
type Color struct {
	Exposure, Contrast, Temp float64
}

// ClampColor mirrors clampColor in timeline.ts exactly.
func ClampColor(c *ColorDef) Color {
	f := func(p *float64, neutral, lo, hi float64) float64 {
		if p == nil {
			return neutral
		}
		return math.Min(hi, math.Max(lo, *p))
	}
	return Color{
		Exposure: f(c.Exposure, 0, -3, 3),
		Contrast: f(c.Contrast, 1, 0, 2),
		Temp:     f(c.Temp, 0, -1, 1),
	}
}

// TransitionDef is the wire form of an out-transition; same tolerant-decode
// posture as ColorDef (junk → defaults, never a log-bricking parse error).
type TransitionDef struct {
	Duration *float64
}

func (t *TransitionDef) UnmarshalJSON(b []byte) error {
	*t = TransitionDef{}
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return nil // non-object → defaults (TS: clampTransition(junk))
	}
	if raw, ok := m["duration"]; ok {
		var v *float64
		if json.Unmarshal(raw, &v) == nil {
			t.Duration = v
		}
	}
	return nil
}

// Transition is the reduced grade. Only dissolve exists in v1 — any wire
// kind normalizes to it (ClampTransition mirrors clampTransition exactly).
type Transition struct {
	Kind     string
	Duration float64
}

func ClampTransition(t *TransitionDef) Transition {
	d := 0.5
	if t.Duration != nil {
		d = math.Min(2, math.Max(0.1, *t.Duration))
	}
	return Transition{Kind: "dissolve", Duration: d}
}

type TrackDef struct {
	ID   string `json:"id"`
	Kind string `json:"kind"` // video | audio | caption
	Name string `json:"name,omitempty"`
}

type ClipDef struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	VersionID  string         `json:"version_id,omitempty"`
	ShotID     string         `json:"shot_id,omitempty"`
	Text       string         `json:"text,omitempty"`
	Color      *ColorDef      `json:"color,omitempty"`
	Transition *TransitionDef `json:"transition,omitempty"`
	Speech     *bool          `json:"speech,omitempty"`
	Start      float64        `json:"start"`
	Duration   float64        `json:"duration"`
	InPoint    *float64       `json:"in_point,omitempty"`
}

type Clip struct {
	ID         string
	Name       string
	VersionID  string
	ShotID     string
	Text       string
	Color      *Color      // nil = neutral
	Transition *Transition // nil = hard cut
	Speech     bool
	Start      float64
	Duration   float64
	InPoint    float64
}

type Track struct {
	ID    string
	Kind  string
	Name  string
	Clips []*Clip
}

type State struct {
	Tracks []*Track
}

// MinClipS mirrors MIN_CLIP_S — one 24fps frame, the reducer's floor.
const MinClipS = 0.04

// ParseOps decodes persisted op payloads. Unknown types are kept (activeOps
// only needs op_id/type/target; Reduce ignores what it doesn't know — same
// forward-compatibility posture as the TS reducer's switch). Duplicate
// op_ids keep the FIRST occurrence — the doc constructors dedup the log the
// same way before reducing, and a duplicate that survives here can change
// the outcome (e.g. a dup move re-applying after its original was rejected).
//
// KNOWN STRICTNESS GAP: wrong-typed SCALAR fields ("start":"x", "text":5)
// hard-fail the whole log here where the TS reducer shrugs — one garbage op
// any client appends bricks export/transcribe for that timeline. ColorDef
// closes this for the color object (tolerant UnmarshalJSON); the scalar
// class needs either append-time payload validation at the API or the same
// leniency per field. Tracked with the security-hardening backlog.
func ParseOps(payloads [][]byte) ([]*Op, error) {
	ops := make([]*Op, 0, len(payloads))
	seen := make(map[string]bool, len(payloads))
	for i, p := range payloads {
		op := &Op{}
		if err := json.Unmarshal(p, op); err != nil {
			return nil, fmt.Errorf("op %d: %w", i, err)
		}
		if op.OpID == "" || op.Type == "" {
			return nil, fmt.Errorf("op %d: missing op_id or type", i)
		}
		if seen[op.OpID] {
			continue
		}
		seen[op.OpID] = true
		ops = append(ops, op)
	}
	return ops, nil
}

// ActiveOps ports doc.ts activeOps exactly: reverse pass where an
// unsuppressed undo suppresses its target only when the target FIRST
// appears at an earlier index — so a suppressed undo never suppresses its
// target ("undoing an undo is redo"), and duplicate op_ids resolve to the
// first occurrence, matching the dedup the docs perform on load.
func ActiveOps(ops []*Op) []*Op {
	firstIndex := make(map[string]int, len(ops))
	for i, op := range ops {
		if _, seen := firstIndex[op.OpID]; !seen {
			firstIndex[op.OpID] = i
		}
	}
	suppressed := make(map[string]bool)
	for i := len(ops) - 1; i >= 0; i-- {
		op := ops[i]
		if op.Type == "undo" && !suppressed[op.OpID] {
			if ti, ok := firstIndex[op.Target]; ok && ti < i {
				suppressed[op.Target] = true
			}
		}
	}
	out := make([]*Op, 0, len(ops))
	for _, op := range ops {
		if op.Type != "undo" && !suppressed[op.OpID] {
			out = append(out, op)
		}
	}
	return out
}

// Reduce ports reduceTimeline: same clamps, same both-or-neither cross-track
// move, same duplicate guards, same stable sort-by-start.
func Reduce(ops []*Op) *State {
	st := &State{}
	findTrack := func(id string) *Track {
		for _, t := range st.Tracks {
			if t.ID == id {
				return t
			}
		}
		return nil
	}
	findClip := func(id string) (*Track, *Clip) {
		for _, t := range st.Tracks {
			for _, c := range t.Clips {
				if c.ID == id {
					return t, c
				}
			}
		}
		return nil, nil
	}
	sortClips := func(t *Track) {
		sort.SliceStable(t.Clips, func(i, j int) bool { return t.Clips[i].Start < t.Clips[j].Start })
	}

	for _, op := range ActiveOps(ops) {
		switch op.Type {
		case "add_track":
			if op.Track == nil || findTrack(op.Track.ID) != nil {
				break
			}
			name := op.Track.Name
			if name == "" {
				switch op.Track.Kind {
				case "video":
					name = "V"
				case "caption":
					name = "C"
				default:
					name = "A"
				}
			}
			t := &Track{ID: op.Track.ID, Kind: op.Track.Kind, Name: name}
			i := len(st.Tracks)
			if op.Index != nil {
				i = max(0, min(*op.Index, len(st.Tracks)))
			}
			st.Tracks = append(st.Tracks[:i], append([]*Track{t}, st.Tracks[i:]...)...)
		case "remove_track":
			for i, t := range st.Tracks {
				if t.ID == op.TrackID {
					st.Tracks = append(st.Tracks[:i], st.Tracks[i+1:]...)
					break
				}
			}
		case "add_clip":
			if op.Clip == nil {
				break
			}
			t := findTrack(op.TrackID)
			if t == nil {
				break
			}
			if _, dup := findClip(op.Clip.ID); dup != nil {
				break
			}
			inPoint := 0.0
			if op.Clip.InPoint != nil {
				inPoint = *op.Clip.InPoint
			}
			var col *Color
			if op.Clip.Color != nil {
				c := ClampColor(op.Clip.Color)
				col = &c
			}
			var trn *Transition
			if op.Clip.Transition != nil {
				t := ClampTransition(op.Clip.Transition)
				trn = &t
			}
			t.Clips = append(t.Clips, &Clip{
				ID:         op.Clip.ID,
				Name:       op.Clip.Name,
				VersionID:  op.Clip.VersionID,
				ShotID:     op.Clip.ShotID,
				Text:       op.Clip.Text,
				Color:      col,
				Transition: trn,
				Speech:     op.Clip.Speech != nil && *op.Clip.Speech,
				Start:      max(0, op.Clip.Start),
				Duration:   max(MinClipS, op.Clip.Duration),
				InPoint:    max(0, inPoint),
			})
			sortClips(t)
		case "remove_clip":
			t, c := findClip(op.ClipID)
			if t == nil {
				break
			}
			for i, cc := range t.Clips {
				if cc == c {
					t.Clips = append(t.Clips[:i], t.Clips[i+1:]...)
					break
				}
			}
		case "move_clip":
			from, clip := findClip(op.ClipID)
			if from == nil || op.Start == nil {
				break
			}
			if op.TrackID != "" && op.TrackID != from.ID {
				to := findTrack(op.TrackID)
				if to == nil || to.Kind != from.Kind {
					break
				}
				clip.Start = max(0, *op.Start)
				for i, cc := range from.Clips {
					if cc == clip {
						from.Clips = append(from.Clips[:i], from.Clips[i+1:]...)
						break
					}
				}
				to.Clips = append(to.Clips, clip)
				sortClips(to)
				break
			}
			clip.Start = max(0, *op.Start)
			sortClips(from)
		case "trim_clip":
			t, clip := findClip(op.ClipID)
			if t == nil {
				break
			}
			if op.Start != nil {
				clip.Start = max(0, *op.Start)
			}
			if op.Duration != nil {
				clip.Duration = max(MinClipS, *op.Duration)
			}
			if op.InPoint != nil {
				clip.InPoint = max(0, *op.InPoint)
			}
			sortClips(t)
		case "set_clip_text":
			_, clip := findClip(op.ClipID)
			if clip == nil {
				break
			}
			clip.Text = ""
			if op.Text != nil {
				clip.Text = *op.Text
			}
		case "set_clip_color":
			_, clip := findClip(op.ClipID)
			if clip == nil {
				break
			}
			clip.Color = nil
			if op.Color != nil {
				c := ClampColor(op.Color)
				clip.Color = &c
			}
		case "set_clip_transition":
			_, clip := findClip(op.ClipID)
			if clip == nil {
				break
			}
			clip.Transition = nil
			if op.Transition != nil {
				t := ClampTransition(op.Transition)
				clip.Transition = &t
			}
		case "set_clip_speech":
			_, clip := findClip(op.ClipID)
			if clip == nil {
				break
			}
			clip.Speech = op.Speech != nil && *op.Speech
		}
	}
	return st
}

// Duration mirrors timelineDuration: end of the last clip.
func Duration(st *State) float64 {
	end := 0.0
	for _, t := range st.Tracks {
		for _, c := range t.Clips {
			end = max(end, c.Start+c.Duration)
		}
	}
	return end
}
