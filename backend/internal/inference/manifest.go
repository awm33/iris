package inference

import (
	"encoding/json"
	"fmt"
	"math"
)

// Manifest is the parsed capability manifest (spec/manifest.schema.json).
// Iris validates every outgoing job against it — defense in depth behind the
// UI's capability-adaptive controls, and the *only* gate for API callers.
type Manifest struct {
	SpecVersion string   `json:"spec_version"`
	ID          string   `json:"id"`
	Family      string   `json:"family"`
	Version     string   `json:"version"`
	Modality    string   `json:"modality"`
	Tasks       []string `json:"tasks"`
	Profiles    map[string]struct {
		MaxWidth  int `json:"max_width"`
		MaxHeight int `json:"max_height"`
	} `json:"profiles"`
	Duration *struct {
		MinS float64 `json:"min_s"`
		MaxS float64 `json:"max_s"`
	} `json:"duration"`
	References map[string]struct {
		Max   int      `json:"max"`
		Roles []string `json:"roles"`
	} `json:"references"`
	Conditioning struct {
		FirstFrame bool `json:"first_frame"`
		LastFrame  bool `json:"last_frame"`
		Keyframes  *struct {
			Max int `json:"max"`
		} `json:"keyframes"`
		DepthSequence *struct {
			Formats []string `json:"formats"`
		} `json:"depth_sequence"`
		PoseSequence bool `json:"pose_sequence"`
		Mask         bool `json:"mask"`
		SourceVideo  bool `json:"source_video"`
		MultiView    bool `json:"multi_view"`
	} `json:"conditioning"`
	Features struct {
		Seed           bool `json:"seed"`
		NegativePrompt bool `json:"negative_prompt"`
	} `json:"features"`
	Pricing struct {
		Unit      string             `json:"unit"`
		Estimates map[string]float64 `json:"estimates"`
	} `json:"pricing"`
	Limits struct {
		Concurrency int `json:"concurrency"`
	} `json:"limits"`
}

func ParseManifest(raw []byte) (*Manifest, error) {
	m := &Manifest{}
	if err := json.Unmarshal(raw, m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if m.ID == "" || m.Modality == "" || len(m.Tasks) == 0 {
		return nil, fmt.Errorf("manifest missing required fields (id/modality/tasks)")
	}
	return m, nil
}

// ValidationError distinguishes capability violations (user-fixable, never
// retried) from transport errors.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(format string, args ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, args...)}
}

// Validate checks a job request against the manifest. Everything the request
// uses must be declared — the manifest honesty rule (spec §5), enforced.
func (m *Manifest) Validate(req *CreateJobRequest) error {
	if !contains(m.Tasks, req.Task) {
		return invalid("model %s does not support task %q (tasks: %v)", m.ID, req.Task, m.Tasks)
	}
	profile, ok := m.Profiles[req.Profile]
	if !ok {
		return invalid("model %s has no profile %q", m.ID, req.Profile)
	}
	if req.Output != nil {
		// Absolute server-side ceilings independent of the manifest — a
		// compromised manifest declaring max_width 2^31 must not let requests
		// through (defense in depth; hard bounds also catch negatives).
		const hardMaxDim, hardMaxDuration = 8192, 600.0
		// NaN sidesteps every </>/== comparison below (proto3 JSON accepts
		// "NaN" for doubles) — reject explicitly before range checks.
		if math.IsNaN(req.Output.DurationS) || math.IsNaN(req.Output.FPS) {
			return invalid("duration/fps must be numbers")
		}
		if req.Output.FPS < 0 || req.Output.FPS > 240 {
			return invalid("fps %.1f out of bounds", req.Output.FPS)
		}
		if req.Output.Width <= 0 || req.Output.Height <= 0 ||
			req.Output.Width > hardMaxDim || req.Output.Height > hardMaxDim {
			return invalid("output dimensions %dx%d out of bounds", req.Output.Width, req.Output.Height)
		}
		if req.Output.Width > profile.MaxWidth || req.Output.Height > profile.MaxHeight {
			return invalid("output %dx%d exceeds profile %q max %dx%d",
				req.Output.Width, req.Output.Height, req.Profile, profile.MaxWidth, profile.MaxHeight)
		}
		if req.Output.DurationS < 0 || req.Output.DurationS > hardMaxDuration {
			return invalid("duration %.1fs out of bounds", req.Output.DurationS)
		}
		if req.Output.DurationS > 0 {
			if m.Duration == nil {
				return invalid("model %s does not declare video duration support", m.ID)
			}
			if req.Output.DurationS < m.Duration.MinS || req.Output.DurationS > m.Duration.MaxS {
				return invalid("duration %.1fs outside model range [%.1f, %.1f]",
					req.Output.DurationS, m.Duration.MinS, m.Duration.MaxS)
			}
		}
	}
	// Video generation without a duration would dispatch with the length
	// unspecified — a deferred failure (or surprise-length clip) on a real
	// endpoint. duration=0 must not sneak past as "not video".
	if m.Modality == "video" && (req.Output == nil || req.Output.DurationS <= 0) {
		return invalid("video generation requires a duration")
	}
	if req.Seed != 0 && !m.Features.Seed {
		return invalid("model %s does not support seeds", m.ID)
	}
	if req.NegativePrompt != "" && !m.Features.NegativePrompt {
		return invalid("model %s does not support negative prompts", m.ID)
	}

	counts := map[string]int{}
	for _, r := range req.References {
		decl, ok := m.References[r.Kind]
		if !ok {
			return invalid("model %s does not accept %s references", m.ID, r.Kind)
		}
		if !contains(decl.Roles, r.Role) {
			return invalid("model %s does not accept %s reference role %q (roles: %v)",
				m.ID, r.Kind, r.Role, decl.Roles)
		}
		counts[r.Kind]++
		if counts[r.Kind] > decl.Max {
			return invalid("too many %s references: model %s allows %d", r.Kind, m.ID, decl.Max)
		}
	}

	if c := req.Conditioning; c != nil {
		switch {
		case c.FirstFrame != nil && !m.Conditioning.FirstFrame:
			return invalid("model %s does not support first_frame conditioning", m.ID)
		case c.LastFrame != nil && !m.Conditioning.LastFrame:
			return invalid("model %s does not support last_frame conditioning", m.ID)
		case len(c.Keyframes) > 0 && m.Conditioning.Keyframes == nil:
			return invalid("model %s does not support keyframe conditioning", m.ID)
		case m.Conditioning.Keyframes != nil && len(c.Keyframes) > m.Conditioning.Keyframes.Max:
			return invalid("too many keyframes: model %s allows %d", m.ID, m.Conditioning.Keyframes.Max)
		case c.DepthSequence != nil && m.Conditioning.DepthSequence == nil:
			return invalid("model %s does not support depth_sequence conditioning", m.ID)
		case c.SourceVideo != nil && !m.Conditioning.SourceVideo:
			return invalid("model %s does not support source_video input", m.ID)
		case c.Mask != nil && !m.Conditioning.Mask:
			return invalid("model %s does not support mask conditioning", m.ID)
		}
	}
	// TODO(M2 follow-up): validate req.Params against manifest.params_schema
	// (JSON Schema) — the advanced panel needs it before exposure in the UI.
	return nil
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
