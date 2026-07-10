package adapters

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
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

// bfl translates our inference-api spec to the Black Forest Labs FLUX API
// (shapes recorded from api.bfl.ai/openapi.json on 2026-07-10 — VERIFY
// against the live API before real keys, per the standing discipline).
// One endpoint, two model families routed by task:
//
//	t2i     → POST /v1/flux-2-pro        (FLUX.2: prompt + up to 10 refs + seed)
//	inpaint → POST /v1/flux-pro-1.0-fill (FLUX.1 Fill: b64 image + b64 mask,
//	                                      white = inpaint — OUR mask semantics)
//
// Lifecycle: submit returns {id, polling_url}; poll the RETURNED URL (the
// docs are explicit: never construct it). Results are region-specific
// signed URLs valid ~10 MINUTES with CORS disabled — the custody bridge is
// mandatory and must run inside the poll that observes Ready.
//
// Inputs are BASE64: our references/conditioning arrive as presigned URLs,
// so CreateJob downloads them (size-capped) and inlines. Auth: x-key.
type bfl struct {
	baseURL string
	token   string
	http    *http.Client

	mu   sync.Mutex
	jobs map[string]*bflJob // our job id → remote polling state
}

type bflJob struct {
	PollingURL string
	Upload     *inference.Upload
}

func newBFL(baseURL, token string) *bfl {
	return &bfl{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 120 * time.Second},
		jobs:    map[string]*bflJob{},
	}
}

// maxInlineInputBytes caps each downloaded input we re-encode as base64.
// FLUX documents ~20MP inputs; 32MB of PNG clears that with headroom, and
// an oversize input errors loudly rather than truncating (custody rule,
// input-side).
const maxInlineInputBytes = 32 << 20

var bflManifest = json.RawMessage(`{
  "spec_version": "1.0",
  "id": "bfl-flux",
  "family": "flux",
  "version": "2.0",
  "modality": "image",
  "tasks": ["t2i", "inpaint"],
  "profiles": {
    "draft":  {"max_width": 1440, "max_height": 1440},
    "master": {"max_width": 2048, "max_height": 2048}
  },
  "references": {
    "image": {"max": 10, "roles": ["character", "object", "style"]}
  },
  "conditioning": {"source_image": true, "mask": true},
  "features": {"prompt": true, "seed": true},
  "params_schema": {
    "type": "object",
    "additionalProperties": false,
    "properties": {
      "output_format": {
        "type": "string",
        "enum": ["png", "jpeg"],
        "description": "Artifact encoding (default png)"
      }
    }
  },
  "pricing": {"unit": "usd_per_job", "estimates": {"draft": 0.03, "master": 0.06}},
  "limits": {"concurrency": 6, "max_queue": 64}
}`)

func (b *bfl) GetManifest(ctx context.Context) (json.RawMessage, error) {
	// Health = an authenticated ping. The mock serves /v1/ping; choosing
	// the live health probe (credits? a HEAD?) is part of the live-key
	// verification pass. Strict 2xx: a 401 must read unhealthy.
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, b.baseURL+"/v1/ping", nil)
	req.Header.Set("x-key", b.token)
	res, err := b.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bfl unreachable: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("bfl ping %d", res.StatusCode)
	}
	return bflManifest, nil
}

// inlineInput downloads a presigned input URL and returns it base64-encoded
// (the BFL API takes inline images, not URLs).
func (b *bfl) inlineInput(ctx context.Context, url, what string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	res, err := b.http.Do(req)
	if err != nil {
		return "", &inference.JobError{Code: "transient", Retryable: true,
			Message: fmt.Sprintf("bfl: fetch %s input: %v", what, err)}
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", &inference.JobError{Code: "transient", Retryable: true,
			Message: fmt.Sprintf("bfl: fetch %s input: %d", what, res.StatusCode)}
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxInlineInputBytes+1))
	if err != nil {
		return "", &inference.JobError{Code: "transient", Retryable: true,
			Message: fmt.Sprintf("bfl: read %s input: %v", what, err)}
	}
	if len(data) > maxInlineInputBytes {
		return "", &inference.ValidationError{Msg: fmt.Sprintf("bfl: %s input exceeds %dMB", what, maxInlineInputBytes>>20)}
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

func (b *bfl) CreateJob(ctx context.Context, req *inference.CreateJobRequest) (*inference.JobStatus, error) {
	outputFormat := "png"
	if len(req.Params) > 0 {
		var p struct {
			OutputFormat string `json:"output_format"`
		}
		if json.Unmarshal(req.Params, &p) == nil && p.OutputFormat != "" {
			outputFormat = p.OutputFormat
		}
	}

	var path string
	body := map[string]any{
		"prompt":        req.Prompt,
		"output_format": outputFormat,
	}
	if req.Seed != 0 {
		body["seed"] = req.Seed
	}
	switch req.Task {
	case "t2i":
		path = "/v1/flux-2-pro"
		if req.Output != nil && req.Output.Width > 0 && req.Output.Height > 0 {
			body["width"] = req.Output.Width
			body["height"] = req.Output.Height
		}
		// FLUX.2 multi-reference: input_image, input_image_2, … (recorded
		// shape — verify the exact field names against the live spec).
		n := 0
		for _, ref := range req.References {
			if ref.Kind != "image" {
				continue
			}
			n++
			if n > 10 {
				return nil, &inference.ValidationError{Msg: "bfl: more than 10 image references"}
			}
			b64, err := b.inlineInput(ctx, ref.URL, fmt.Sprintf("reference %d", n))
			if err != nil {
				return nil, err
			}
			key := "input_image"
			if n > 1 {
				key = fmt.Sprintf("input_image_%d", n)
			}
			body[key] = b64
		}
	case "inpaint":
		path = "/v1/flux-pro-1.0-fill"
		if req.Conditioning == nil || req.Conditioning.SourceImage == nil || req.Conditioning.Mask == nil {
			return nil, &inference.ValidationError{Msg: "bfl: inpaint requires source_image and mask"}
		}
		img, err := b.inlineInput(ctx, req.Conditioning.SourceImage.URL, "source_image")
		if err != nil {
			return nil, err
		}
		mask, err := b.inlineInput(ctx, req.Conditioning.Mask.URL, "mask")
		if err != nil {
			return nil, err
		}
		// Mask semantics align exactly: white = inpaint = our "white =
		// generate" spec — pass through untouched.
		body["image"] = img
		body["mask"] = mask
	default:
		return nil, &inference.ValidationError{Msg: fmt.Sprintf("bfl: unsupported task %q", req.Task)}
	}

	payload, _ := json.Marshal(body)
	post, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	post.Header.Set("x-key", b.token)
	post.Header.Set("Content-Type", "application/json")
	res, err := b.http.Do(post)
	if err != nil {
		// Post-send timeouts may have cost money server-side — taxonomy
		// transient, never the free unreachable fast-path (seedance rule).
		if isPostSendTimeout(err) {
			return nil, &inference.JobError{Code: "transient", Retryable: true,
				Message: fmt.Sprintf("bfl: submit timed out after send: %v", err)}
		}
		return nil, err
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return nil, &inference.JobError{Code: "invalid_input", Retryable: false,
			Message: "bfl auth failed — check the endpoint key"}
	case res.StatusCode == http.StatusUnprocessableEntity || res.StatusCode == http.StatusBadRequest:
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return nil, &inference.ValidationError{Msg: fmt.Sprintf("bfl rejected the request: %s", strings.TrimSpace(string(msg)))}
	case res.StatusCode == http.StatusTooManyRequests:
		return nil, &inference.JobError{Code: "overloaded", Retryable: true, Message: "bfl rate limited"}
	case res.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("bfl submit: %d", res.StatusCode)
	}

	var created struct {
		ID         string `json:"id"`
		PollingURL string `json:"polling_url"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&created); err != nil {
		return nil, &inference.JobError{Code: "transient", Retryable: true,
			Message: fmt.Sprintf("bfl: unreadable submit response: %v", err)}
	}
	if created.PollingURL == "" {
		return nil, &inference.JobError{Code: "transient", Retryable: true,
			Message: "bfl: submit response missing polling_url"}
	}
	b.mu.Lock()
	b.jobs[req.ID] = &bflJob{PollingURL: created.PollingURL, Upload: req.Upload}
	b.mu.Unlock()
	return &inference.JobStatus{ID: req.ID, State: "queued"}, nil
}

func (b *bfl) GetJob(ctx context.Context, id string) (*inference.JobStatus, error) {
	b.mu.Lock()
	job := b.jobs[id]
	b.mu.Unlock()
	if job == nil {
		// In-memory mapping lost (restart) — re-dispatch handles it today;
		// PR 41 persists the remote polling_url on the job row.
		return nil, fmt.Errorf("bfl: unknown job %s", id)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, job.PollingURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-key", b.token)
	res, err := b.http.Do(req)
	if err != nil {
		return nil, &inference.JobError{Code: "transient", Retryable: true,
			Message: fmt.Sprintf("bfl: poll: %v", err)}
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, &inference.JobError{Code: "transient", Retryable: true,
			Message: fmt.Sprintf("bfl: poll: %d", res.StatusCode)}
	}
	var polled struct {
		Status string `json:"status"`
		Result struct {
			Sample string `json:"sample"`
		} `json:"result"`
		Details json.RawMessage `json:"details"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&polled); err != nil {
		return nil, &inference.JobError{Code: "transient", Retryable: true,
			Message: fmt.Sprintf("bfl: unreadable poll response: %v", err)}
	}

	switch polled.Status {
	case "Pending", "Reasoning":
		return &inference.JobStatus{ID: id, State: "running", Progress: 0.2}, nil
	case "Generating":
		return &inference.JobStatus{ID: id, State: "running", Progress: 0.6}, nil
	case "Request Moderated", "Content Moderated":
		// Distinct statuses in BFL's enum — our safety taxonomy, terminal.
		return nil, &inference.JobError{Code: "safety_blocked", Retryable: false,
			Message: fmt.Sprintf("bfl moderation: %s", polled.Status)}
	case "Error":
		return nil, &inference.JobError{Code: "invalid_input", Retryable: false,
			Message: fmt.Sprintf("bfl error: %s", strings.TrimSpace(string(polled.Details)))}
	case "Task not found":
		return nil, &inference.JobError{Code: "invalid_input", Retryable: false,
			Message: "bfl: task expired or unknown at the provider"}
	case "Ready":
		// Fall through to custody below.
	default:
		// Open set: BFL's own examples check statuses absent from the
		// enum. Unknown = still working; the lease/timeout machinery
		// bounds how long we'll believe that.
		return &inference.JobStatus{ID: id, State: "running", Progress: 0.5}, nil
	}

	if polled.Result.Sample == "" {
		return nil, &inference.JobError{Code: "transient", Retryable: true,
			Message: "bfl: Ready without result.sample"}
	}
	// Custody bridge — inside THIS poll: delivery URLs live ~10 minutes.
	art, err := b.bridgeArtifact(ctx, polled.Result.Sample, job.Upload)
	if err != nil {
		return nil, err
	}
	st := &inference.JobStatus{
		ID: id, State: "complete", Progress: 1,
		Artifacts: []inference.Artifact{*art},
		Metrics:   &inference.Metrics{GPUSeconds: 1},
	}
	return st, nil
}

// bridgeArtifact downloads the (short-lived) delivery URL and re-uploads to
// our presigned target — same discipline as the seedance bridge: oversize
// errors, never truncates.
func (b *bfl) bridgeArtifact(ctx context.Context, url string, upload *inference.Upload) (*inference.Artifact, error) {
	get, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := b.http.Do(get)
	if err != nil {
		return nil, &inference.JobError{Code: "transient", Retryable: true,
			Message: fmt.Sprintf("bfl: fetch result: %v", err)}
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, &inference.JobError{Code: "transient", Retryable: true,
			Message: fmt.Sprintf("bfl: fetch result: %d", res.StatusCode)}
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxArtifactBytes+1))
	if err != nil {
		return nil, &inference.JobError{Code: "transient", Retryable: true,
			Message: fmt.Sprintf("bfl: read result: %v", err)}
	}
	if int64(len(data)) > maxArtifactBytes {
		return nil, fmt.Errorf("bfl result exceeds %dMB cap", maxArtifactBytes>>20)
	}
	contentType := res.Header.Get("Content-Type")
	if contentType == "" || !strings.HasPrefix(contentType, "image/") {
		contentType = "image/png"
	}

	uploaded := false
	if upload != nil && len(upload.Artifacts) > 0 && upload.Artifacts[0].PutURL != "" {
		if upload.Artifacts[0].ContentType != "" {
			contentType = upload.Artifacts[0].ContentType
		}
		put, perr := http.NewRequestWithContext(ctx, http.MethodPut, upload.Artifacts[0].PutURL, bytes.NewReader(data))
		if perr != nil {
			return nil, perr
		}
		put.Header.Set("Content-Type", contentType)
		pres, perr := b.http.Do(put)
		if perr != nil {
			return nil, &inference.JobError{Code: "transient", Retryable: true,
				Message: fmt.Sprintf("bfl: artifact upload after success: %v", perr)}
		}
		pres.Body.Close()
		if pres.StatusCode >= 300 {
			return nil, &inference.JobError{Code: "transient", Retryable: true,
				Message: fmt.Sprintf("bfl: artifact upload after success: %d", pres.StatusCode)}
		}
		uploaded = true
	}
	sum := sha256.Sum256(data)
	return &inference.Artifact{ContentType: contentType, Uploaded: uploaded, SHA256: hex.EncodeToString(sum[:])}, nil
}

func (b *bfl) CancelJob(ctx context.Context, id string) error {
	// No cancel endpoint in the recorded spec; generations are short.
	return nil
}
