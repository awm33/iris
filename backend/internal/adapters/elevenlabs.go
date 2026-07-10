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

// elevenlabs translates our inference-api spec to the ElevenLabs TTS API
// (RECORDED shapes — verify against the live API before real keys). The W4
// dialogue workflow's voice source: prompt text → speech audio.
//
// Unlike Seedance, TTS is SYNCHRONOUS: the create call returns the audio
// bytes directly — CreateJob performs the whole generation + custody
// bridge and returns a terminal status; GetJob only replays it.
type elevenlabs struct {
	baseURL string
	token   string
	http    *http.Client

	mu   sync.Mutex
	done map[string]*inference.JobStatus // attempt id → terminal status
}

func newElevenLabs(baseURL, token string) *elevenlabs {
	return &elevenlabs{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 120 * time.Second},
		done:    map[string]*inference.JobStatus{},
	}
}

// defaultVoice: per-character voice refs land with the W4 slice (voice
// params via params_schema); until then every generation uses one voice.
const defaultVoice = "iris-narrator"

var elevenLabsManifest = json.RawMessage(`{
  "spec_version": "1.0",
  "id": "elevenlabs-tts-v2",
  "family": "elevenlabs",
  "version": "2.0",
  "modality": "audio",
  "tasks": ["tts"],
  "profiles": {
    "draft":  {"max_width": 0, "max_height": 0},
    "master": {"max_width": 0, "max_height": 0}
  },
  "references": {},
  "conditioning": {},
  "params_schema": {
    "type": "object",
    "additionalProperties": false,
    "properties": {
      "voice_id": {
        "type": "string",
        "enum": ["iris-narrator", "mara"],
        "description": "ElevenLabs voice — character voice refs bind here (W4)"
      }
    }
  },
  "features": {"prompt": true, "seed": false},
  "pricing": {"unit": "usd_per_job", "estimates": {"draft": 0.05, "master": 0.15}},
  "limits": {"concurrency": 4, "max_queue": 64}
}`)

func (e *elevenlabs) GetManifest(ctx context.Context) (json.RawMessage, error) {
	// Health = the voices listing answering with our key (xi-api-key auth,
	// their scheme). Strict 2xx: a 401 must read unhealthy, not healthy.
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, e.baseURL+"/v1/voices", nil)
	req.Header.Set("xi-api-key", e.token)
	res, err := e.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs unreachable: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("elevenlabs voices %d", res.StatusCode)
	}
	return elevenLabsManifest, nil
}

func (e *elevenlabs) CreateJob(ctx context.Context, req *inference.CreateJobRequest) (*inference.JobStatus, error) {
	voice := defaultVoice
	if len(req.Params) > 0 {
		var p struct {
			VoiceID string `json:"voice_id"`
		}
		if json.Unmarshal(req.Params, &p) == nil && p.VoiceID != "" {
			voice = p.VoiceID
		}
	}
	body, _ := json.Marshal(map[string]any{
		"text":     req.Prompt,
		"model_id": "eleven_multilingual_v2",
	})
	post, err := http.NewRequestWithContext(ctx, http.MethodPost,
		e.baseURL+"/v1/text-to-speech/"+voice, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	post.Header.Set("xi-api-key", e.token)
	post.Header.Set("Content-Type", "application/json")
	res, err := e.http.Do(post)
	if err != nil {
		// A timeout here is AFTER the request was sent — for a synchronous
		// vendor the generation may have run (money spent). The unreachable
		// fast-path would re-submit a paid TTS per loop at zero attempt
		// cost; a taxonomy transient burns attempts and parks honestly.
		// Pre-send dial failures stay raw: genuinely free, unreachable is
		// the right classification.
		if isPostSendTimeout(err) {
			return nil, &inference.JobError{Code: "transient", Retryable: true,
				Message: fmt.Sprintf("elevenlabs tts timed out after send: %v", err)}
		}
		return nil, err
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return nil, &inference.JobError{Code: "invalid_input", Retryable: false,
			Message: "elevenlabs auth failed — check the endpoint key"}
	case res.StatusCode == http.StatusUnprocessableEntity || res.StatusCode == http.StatusBadRequest:
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return nil, &inference.ValidationError{Msg: fmt.Sprintf("elevenlabs rejected the request: %s", strings.TrimSpace(string(b)))}
	case res.StatusCode == http.StatusTooManyRequests:
		return nil, &inference.JobError{Code: "overloaded", Retryable: true, Message: "elevenlabs rate limited"}
	case res.StatusCode == http.StatusNotFound:
		// Unknown voice id: retrying can't fix it — park with the cause.
		return nil, &inference.JobError{Code: "invalid_input", Retryable: false,
			Message: fmt.Sprintf("elevenlabs voice %q not found", voice)}
	case res.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("elevenlabs tts: %d", res.StatusCode)
	}

	if ct := res.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "audio/") {
		// A 200 with a non-audio body (JSON error page) must not land as a
		// "verified" audio asset that only fails later at probe.
		return nil, fmt.Errorf("elevenlabs returned %q, want audio/*", ct)
	}
	// Synchronous response IS the artifact — same truncation discipline as
	// every custody bridge: an oversize body errors, never silently cuts.
	data, err := io.ReadAll(io.LimitReader(res.Body, maxArtifactBytes+1))
	if err != nil {
		return nil, &inference.JobError{Code: "transient", Retryable: true,
			Message: fmt.Sprintf("elevenlabs audio read: %v", err)}
	}
	if int64(len(data)) > maxArtifactBytes {
		return nil, fmt.Errorf("elevenlabs result exceeds %dMB cap", maxArtifactBytes>>20)
	}
	sum := sha256.Sum256(data)

	contentType := "audio/mpeg"
	uploaded := false
	if req.Upload != nil && len(req.Upload.Artifacts) > 0 && req.Upload.Artifacts[0].PutURL != "" {
		if req.Upload.Artifacts[0].ContentType != "" {
			contentType = req.Upload.Artifacts[0].ContentType
		}
		put, perr := http.NewRequestWithContext(ctx, http.MethodPut,
			req.Upload.Artifacts[0].PutURL, bytes.NewReader(data))
		if perr != nil {
			return nil, perr
		}
		put.Header.Set("Content-Type", contentType)
		pres, perr := e.http.Do(put)
		if perr != nil {
			// Taxonomy transient, never a *url.Error chain: the orchestrator's
			// unreachable fast-path would re-submit a paid generation per loop.
			return nil, &inference.JobError{Code: "transient", Retryable: true,
				Message: fmt.Sprintf("artifact upload after tts success: %v", perr)}
		}
		pres.Body.Close()
		if pres.StatusCode >= 300 {
			return nil, &inference.JobError{Code: "transient", Retryable: true,
				Message: fmt.Sprintf("artifact upload after tts success: %d", pres.StatusCode)}
		}
		uploaded = true
	}

	st := &inference.JobStatus{
		ID: req.ID, State: "complete", Progress: 1,
		Artifacts: []inference.Artifact{{
			ContentType: contentType,
			Uploaded:    uploaded,
			SHA256:      hex.EncodeToString(sum[:]),
		}},
		// usd_per_job pricing: the billable quantity is one job; character
		// count is the vendor's internal unit and lands with real billing.
		Metrics: &inference.Metrics{GPUSeconds: 1},
	}
	e.mu.Lock()
	e.done[req.ID] = st
	e.mu.Unlock()
	return st, nil
}

func (e *elevenlabs) GetJob(ctx context.Context, id string) (*inference.JobStatus, error) {
	e.mu.Lock()
	st := e.done[id]
	e.mu.Unlock()
	if st == nil {
		// Synchronous adapter: a status the orchestrator didn't get from
		// CreateJob directly means the attempt died — re-dispatch handles it.
		return nil, fmt.Errorf("elevenlabs: unknown job %s", id)
	}
	return st, nil
}

func (e *elevenlabs) CancelJob(ctx context.Context, id string) error {
	return nil // synchronous: by the time anyone cancels, it's done
}
