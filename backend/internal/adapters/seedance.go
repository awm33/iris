package adapters

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/awm33/iris/backend/internal/inference"
)

// seedance translates our inference-api spec to the Seedance generation API
// (ByteDance Ark v3 contents/generations shapes, RECORDED from docs — the
// dev mock replicates these; verify against the live API before real keys).
//
// Two impedance gaps this adapter bridges:
//   - Artifacts: Seedance RETURNS a result URL; our spec expects the
//     endpoint to PUT into a presigned target. On success the adapter
//     downloads and re-uploads server-side — the commercial proxy path
//     (key custody + artifact custody in one place).
//   - Error taxonomy: their status/error strings map onto our
//     invalid_input / safety_blocked / transient codes so retry/park
//     behavior matches every other endpoint.
type seedance struct {
	baseURL string
	token   string
	http    *http.Client

	mu   sync.Mutex
	jobs map[string]*seedanceJob // our attempt id → remote state
}

type seedanceJob struct {
	remoteID    string
	putURL      string
	contentType string
	// billableS: the manifest prices usd_per_second — output duration is
	// the billable quantity, reported via Metrics at completion.
	billableS float64
	// terminal caches the mapped final status so repeated polls after
	// upload don't re-download the artifact.
	terminal *inference.JobStatus
}

// Var, not const: tests shrink it to exercise the truncation boundary.
var maxArtifactBytes int64 = 512 << 20

func newSeedance(baseURL, token string) *seedance {
	return &seedance{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 120 * time.Second},
		jobs:    map[string]*seedanceJob{},
	}
}

// Static manifest: Seedance doesn't serve our spec — the adapter declares
// what the model can do. Lip-sync-in-gen is the M6 reason this adapter
// exists (the only in-gen path until R&D's audio-conditioned Wan lands).
var seedanceManifest = json.RawMessage(`{
  "spec_version": "1.0",
  "id": "seedance-1-0-pro",
  "family": "seedance",
  "version": "1.0",
  "modality": "video",
  "tasks": ["t2v", "i2v"],
  "profiles": {
    "draft":  {"max_width": 864,  "max_height": 480},
    "master": {"max_width": 1920, "max_height": 1080}
  },
  "duration": {"min_s": 3, "max_s": 12},
  "references": {"audio": {"max": 1, "roles": ["speech_lipsync"]}},
  "conditioning": {"first_frame": true},
  "features": {"prompt": true, "seed": true, "lip_sync_in_gen": true, "audio_gen": true},
  "pricing": {"unit": "usd_per_second", "estimates": {"draft": 0.12, "master": 0.45}},
  "limits": {"concurrency": 4, "max_queue": 64}
}`)

func (s *seedance) GetManifest(ctx context.Context) (json.RawMessage, error) {
	// Health ride-along: the manifest is static, but reporting it healthy
	// requires the remote (mock in dev) to actually answer.
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/api/v3/ping", nil)
	req.Header.Set("Authorization", "Bearer "+s.token)
	res, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("seedance unreachable: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		// 401/403 especially: a bad key must make the endpoint UNHEALTHY,
		// not healthy-until-every-job-fails.
		return nil, fmt.Errorf("seedance ping %d", res.StatusCode)
	}
	return seedanceManifest, nil
}

// --- recorded API shapes ---

type sdContent struct {
	Type     string `json:"type"` // text | image_url | audio_url
	Text     string `json:"text,omitempty"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url,omitempty"`
	AudioURL *struct {
		URL string `json:"url"`
	} `json:"audio_url,omitempty"`
	Role string `json:"role,omitempty"` // first_frame | lip_sync
}

type sdCreate struct {
	Model   string      `json:"model"`
	Content []sdContent `json:"content"`
}

type sdTask struct {
	ID     string `json:"id"`
	Status string `json:"status"` // queued | running | succeeded | failed | cancelled
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
	Content *struct {
		VideoURL string `json:"video_url"`
	} `json:"content,omitempty"`
	Usage *struct {
		CompletionTokens int64 `json:"completion_tokens"`
	} `json:"usage,omitempty"`
}

func (s *seedance) CreateJob(ctx context.Context, req *inference.CreateJobRequest) (*inference.JobStatus, error) {
	// Seedance takes generation params as text-command suffixes (recorded
	// convention) rather than structured fields.
	text := req.Prompt
	if out := req.Output; out != nil {
		if out.DurationS > 0 {
			text += fmt.Sprintf(" --duration %d", int(out.DurationS+0.5))
		}
		switch {
		case out.Height >= 1080:
			text += " --resolution 1080p"
		case out.Height >= 720:
			text += " --resolution 720p"
		default:
			text += " --resolution 480p"
		}
	}
	if req.Seed != 0 {
		text += fmt.Sprintf(" --seed %d", req.Seed)
	}
	body := sdCreate{Model: "seedance-1-0-pro", Content: []sdContent{{Type: "text", Text: text}}}
	// Audio references drive in-gen lip sync — the M6 reason this adapter
	// exists. Our spec role is speech_lipsync; the RECORDED wire shape uses
	// their "lip_sync" role on an audio_url content item.
	for _, ref := range req.References {
		if ref.Kind != "audio" || ref.URL == "" {
			continue
		}
		item := sdContent{Type: "audio_url", Role: "lip_sync"}
		item.AudioURL = &struct {
			URL string `json:"url"`
		}{URL: ref.URL}
		body.Content = append(body.Content, item)
	}
	if c := req.Conditioning; c != nil && c.FirstFrame != nil && c.FirstFrame.URL != "" {
		img := sdContent{Type: "image_url", Role: "first_frame"}
		img.ImageURL = &struct {
			URL string `json:"url"`
		}{URL: c.FirstFrame.URL}
		body.Content = append(body.Content, img)
	}

	var task sdTask
	if err := s.call(ctx, http.MethodPost, "/api/v3/contents/generations/tasks", body, &task); err != nil {
		return nil, err
	}
	if task.ID == "" {
		return nil, &inference.ValidationError{Msg: "seedance returned no task id"}
	}
	putURL, contentType := "", "video/mp4"
	if req.Upload != nil && len(req.Upload.Artifacts) > 0 {
		putURL = req.Upload.Artifacts[0].PutURL
		if req.Upload.Artifacts[0].ContentType != "" {
			contentType = req.Upload.Artifacts[0].ContentType
		}
	}
	s.mu.Lock()
	billable := 0.0
	if req.Output != nil {
		billable = req.Output.DurationS
	}
	s.jobs[req.ID] = &seedanceJob{remoteID: task.ID, putURL: putURL, contentType: contentType, billableS: billable}
	s.mu.Unlock()
	return &inference.JobStatus{ID: req.ID, State: "queued"}, nil
}

func (s *seedance) GetJob(ctx context.Context, id string) (*inference.JobStatus, error) {
	s.mu.Lock()
	j := s.jobs[id]
	s.mu.Unlock()
	if j == nil {
		// Adapter state is per-DISPATCH (a client is built per attempt):
		// a reclaim/restart re-submits a fresh remote task and the old one
		// is never canceled — acceptable against the mock, DOUBLE-BILLING
		// with real keys. Ticketed: persist remoteID on the job row and
		// re-attach instead of re-submitting, before real keys land.
		return nil, fmt.Errorf("seedance: unknown job %s", id)
	}
	if j.terminal != nil {
		return j.terminal, nil
	}

	var task sdTask
	if err := s.call(ctx, http.MethodGet, "/api/v3/contents/generations/tasks/"+j.remoteID, nil, &task); err != nil {
		return nil, err
	}
	switch task.Status {
	case "queued":
		return &inference.JobStatus{ID: id, State: "queued"}, nil
	case "running":
		return &inference.JobStatus{ID: id, State: "running", Progress: 0.5}, nil
	case "cancelled":
		return &inference.JobStatus{ID: id, State: "canceled"}, nil
	case "failed":
		st := &inference.JobStatus{ID: id, State: "failed", Error: mapSeedanceError(task)}
		j.terminal = st
		return st, nil
	case "succeeded":
		if task.Content == nil || task.Content.VideoURL == "" {
			return nil, fmt.Errorf("seedance succeeded without a video url")
		}
		art, err := s.proxyArtifact(ctx, task.Content.VideoURL, j)
		if err != nil {
			// NEVER let a *url.Error reach the orchestrator here: its
			// unreachable fast-path requeues at zero attempt cost and each
			// requeue re-SUBMITS — a paid generation per loop for up to an
			// hour after the money was already spent. Transfer failures are
			// ordinary transients: attempt-costed, bounded, parked honestly.
			return nil, &inference.JobError{Code: "transient", Retryable: true,
				Message: fmt.Sprintf("artifact transfer after remote success: %v", err)}
		}
		st := &inference.JobStatus{
			ID: id, State: "complete", Progress: 1,
			Artifacts: []inference.Artifact{*art},
			Metrics:   &inference.Metrics{GPUSeconds: j.billableS},
		}
		j.terminal = st
		return st, nil
	default:
		return nil, fmt.Errorf("seedance reported unknown status %q", task.Status)
	}
}

func (s *seedance) CancelJob(ctx context.Context, id string) error {
	s.mu.Lock()
	j := s.jobs[id]
	s.mu.Unlock()
	if j == nil {
		return nil
	}
	return s.call(ctx, http.MethodDelete, "/api/v3/contents/generations/tasks/"+j.remoteID, nil, nil)
}

// proxyArtifact downloads the remote result and re-uploads it to our
// presigned target, hashing in transit — the server-side custody bridge.
// The result URL is never surfaced to clients.
func (s *seedance) proxyArtifact(ctx context.Context, videoURL string, j *seedanceJob) (*inference.Artifact, error) {
	get, err := http.NewRequestWithContext(ctx, http.MethodGet, videoURL, nil)
	if err != nil {
		return nil, err
	}
	res, err := s.http.Do(get)
	if err != nil {
		return nil, fmt.Errorf("fetch seedance result: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch seedance result: %d", res.StatusCode)
	}
	// Buffer + hash: results are bounded by duration/resolution caps.
	// LimitReader alone would TRUNCATE silently — and the truncated bytes
	// hash consistently, so the corrupt artifact would land as verified.
	data, err := io.ReadAll(io.LimitReader(res.Body, maxArtifactBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxArtifactBytes {
		return nil, fmt.Errorf("seedance result exceeds %dMB cap", maxArtifactBytes>>20)
	}
	sum := sha256.Sum256(data)

	if j.putURL != "" {
		put, err := http.NewRequestWithContext(ctx, http.MethodPut, j.putURL, bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		put.Header.Set("Content-Type", j.contentType)
		pres, err := s.http.Do(put)
		if err != nil {
			return nil, fmt.Errorf("upload artifact: %w", err)
		}
		defer pres.Body.Close()
		if pres.StatusCode >= 300 {
			return nil, fmt.Errorf("upload artifact: %d", pres.StatusCode)
		}
	}
	return &inference.Artifact{
		ContentType: j.contentType,
		Uploaded:    j.putURL != "",
		SHA256:      hex.EncodeToString(sum[:]),
	}, nil
}

// mapSeedanceError translates their error space into our taxonomy so parks
// and retries behave like every other endpoint.
func mapSeedanceError(task sdTask) *inference.JobError {
	code, msg := "", "seedance failure"
	if task.Error != nil {
		code, msg = task.Error.Code, task.Error.Message
	}
	switch {
	case strings.Contains(code, "content") || strings.Contains(code, "policy") || strings.Contains(code, "sensitive"):
		return &inference.JobError{Code: "safety_blocked", Message: msg, Retryable: false}
	case strings.Contains(code, "invalid") || strings.Contains(code, "param"):
		return &inference.JobError{Code: "invalid_input", Message: msg, Retryable: false}
	default:
		return &inference.JobError{Code: "transient", Message: msg, Retryable: true}
	}
}

func (s *seedance) call(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.baseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := s.http.Do(req)
	if err != nil {
		// Same discipline as elevenlabs: post-send timeouts are taxonomy
		// transients (a submitted task may be running — the unreachable
		// fast-path would re-submit it per loop); dial failures stay raw.
		if isPostSendTimeout(err) {
			return &inference.JobError{Code: "transient", Retryable: true,
				Message: fmt.Sprintf("seedance %s timed out after send: %v", path, err)}
		}
		return err
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusBadRequest || res.StatusCode == http.StatusUnprocessableEntity:
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return &inference.ValidationError{Msg: fmt.Sprintf("seedance rejected the request: %s", strings.TrimSpace(string(b)))}
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		// Retrying auth failures burns attempts for nothing.
		return &inference.JobError{Code: "invalid_input", Retryable: false,
			Message: "seedance auth failed — check the endpoint key"}
	case res.StatusCode == http.StatusTooManyRequests:
		return &inference.JobError{Code: "overloaded", Retryable: true, Message: "seedance rate limited"}
	case res.StatusCode >= 300:
		return fmt.Errorf("seedance %s %s: %d", method, path, res.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(res.Body).Decode(out)
	}
	return nil
}
