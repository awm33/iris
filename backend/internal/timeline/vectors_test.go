package timeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Shared drift vectors: the same files replay through the TS reducer
// (web/packages/doc-runtime/src/vectors.test.ts). A change to either
// reducer that fails here is preview/export divergence — fix the port,
// don't touch the vector.

type vecClip struct {
	ID        string  `json:"id"`
	Start     float64 `json:"start"`
	Duration  float64 `json:"duration"`
	InPoint   float64 `json:"in_point"`
	VersionID string  `json:"version_id"`
	ShotID    string  `json:"shot_id"`
	Text      string  `json:"text"`
	Color     *Color  `json:"color"`
}

type vecTrack struct {
	ID    string    `json:"id"`
	Kind  string    `json:"kind"`
	Name  string    `json:"name"`
	Clips []vecClip `json:"clips"`
}

type vector struct {
	Name     string            `json:"name"`
	Ops      []json.RawMessage `json:"ops"`
	Expected struct {
		Tracks []vecTrack `json:"tracks"`
	} `json:"expected"`
}

func TestVectors(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "spec", "timeline-vectors")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read vectors dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no vectors found")
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			var v vector
			if err := json.Unmarshal(raw, &v); err != nil {
				t.Fatalf("parse vector: %v", err)
			}
			payloads := make([][]byte, len(v.Ops))
			for i, op := range v.Ops {
				payloads[i] = op
			}
			ops, err := ParseOps(payloads)
			if err != nil {
				t.Fatalf("parse ops: %v", err)
			}
			got := normalize(Reduce(ops))
			if !reflect.DeepEqual(got, v.Expected.Tracks) {
				gj, _ := json.MarshalIndent(got, "", "  ")
				ej, _ := json.MarshalIndent(v.Expected.Tracks, "", "  ")
				t.Errorf("%s: reducer diverged\ngot:  %s\nwant: %s", v.Name, gj, ej)
			}
		})
	}
}

func normalize(st *State) []vecTrack {
	out := make([]vecTrack, 0, len(st.Tracks))
	for _, tr := range st.Tracks {
		vt := vecTrack{ID: tr.ID, Kind: tr.Kind, Name: tr.Name, Clips: []vecClip{}}
		for _, c := range tr.Clips {
			vt.Clips = append(vt.Clips, vecClip{
				ID: c.ID, Start: c.Start, Duration: c.Duration,
				InPoint: c.InPoint, VersionID: c.VersionID, ShotID: c.ShotID,
				Text: c.Text, Color: c.Color,
			})
		}
		out = append(out, vt)
	}
	return out
}
