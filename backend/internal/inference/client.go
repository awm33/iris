// Package inference is the typed HTTP client for spec/inference-api.md — the
// one way Iris talks to model endpoints (our fleet, self-hosted open-weight,
// commercial shims). The mock servers and the conformance suite define the
// contract; this client consumes it.
package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func New(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimSuffix(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 60 * time.Second},
	}
}

// ── Request/response shapes (spec §2–§5) ─────────────────────────────────────

type Reference struct {
	Kind   string  `json:"kind"`
	Role   string  `json:"role"`
	URL    string  `json:"url"`
	Weight float64 `json:"weight,omitempty"`
}

type FrameRef struct {
	URL string `json:"url"`
}

type KeyframeAt struct {
	T   float64 `json:"t"`
	URL string  `json:"url"`
}

type SourceVideo struct {
	URL      string  `json:"url"`
	Strength float64 `json:"strength,omitempty"`
}

type Conditioning struct {
	FirstFrame    *FrameRef    `json:"first_frame,omitempty"`
	LastFrame     *FrameRef    `json:"last_frame,omitempty"`
	Keyframes     []KeyframeAt `json:"keyframes,omitempty"`
	DepthSequence *FrameRef    `json:"depth_sequence,omitempty"`
	SourceVideo   *SourceVideo `json:"source_video,omitempty"`
	Mask          *FrameRef    `json:"mask,omitempty"`
}

type Output struct {
	Width     int     `json:"width"`
	Height    int     `json:"height"`
	DurationS float64 `json:"duration_s,omitempty"`
	FPS       float64 `json:"fps,omitempty"`
}

type UploadTarget struct {
	PutURL      string `json:"put_url"`
	ContentType string `json:"content_type"`
}

type Upload struct {
	Artifacts []UploadTarget `json:"artifacts"`
	Thumbnail *UploadTarget  `json:"thumbnail,omitempty"`
}

type CreateJobRequest struct {
	ID             string          `json:"id"`
	Task           string          `json:"task"`
	Profile        string          `json:"profile"`
	Prompt         string          `json:"prompt"`
	NegativePrompt string          `json:"negative_prompt,omitempty"`
	Seed           int64           `json:"seed,omitempty"`
	Output         *Output         `json:"output,omitempty"`
	References     []Reference     `json:"references,omitempty"`
	Conditioning   *Conditioning   `json:"conditioning,omitempty"`
	Params         json.RawMessage `json:"params,omitempty"`
	Upload         *Upload         `json:"upload,omitempty"`
}

type JobError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (e *JobError) Error() string { return e.Code + ": " + e.Message }

type Artifact struct {
	Index       int     `json:"index"`
	ContentType string  `json:"content_type"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	DurationS   float64 `json:"duration_s,omitempty"`
	FPS         float64 `json:"fps,omitempty"`
	Uploaded    bool    `json:"uploaded"`
	SHA256      string  `json:"sha256"`
}

type JobStatus struct {
	ID        string     `json:"id"`
	State     string     `json:"state"` // queued|running|uploading|complete|failed|canceled
	Progress  float64    `json:"progress"`
	EtaS      *int       `json:"eta_s"`
	Artifacts []Artifact `json:"artifacts"`
	Error     *JobError  `json:"error"`
	Metrics   *struct {
		GPUSeconds float64 `json:"gpu_seconds"`
	} `json:"metrics"`
}

func (s *JobStatus) Terminal() bool {
	switch s.State {
	case "complete", "failed", "canceled":
		return true
	}
	return false
}

// ── Calls ─────────────────────────────────────────────────────────────────────

func (c *Client) GetManifest(ctx context.Context) (json.RawMessage, error) {
	var out json.RawMessage
	return out, c.do(ctx, http.MethodGet, "/v1/manifest", nil, &out)
}

func (c *Client) CreateJob(ctx context.Context, req *CreateJobRequest) (*JobStatus, error) {
	var out JobStatus
	return &out, c.do(ctx, http.MethodPost, "/v1/jobs", req, &out)
}

func (c *Client) GetJob(ctx context.Context, id string) (*JobStatus, error) {
	var out JobStatus
	return &out, c.do(ctx, http.MethodGet, "/v1/jobs/"+id, nil, &out)
}

func (c *Client) CancelJob(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/jobs/"+id, nil, nil)
}

func (c *Client) Healthz(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/v1/healthz", nil, nil)
}

// do performs the request; non-2xx responses are decoded into *JobError when
// the body carries the spec's error envelope (callers use errors.As to read
// the taxonomy).
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var envelope struct {
			Error *JobError `json:"error"`
		}
		if json.Unmarshal(data, &envelope) == nil && envelope.Error != nil {
			return envelope.Error
		}
		return fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, truncate(data, 300))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode %s response: %w", path, err)
		}
	}
	return nil
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "…"
	}
	return string(b)
}
