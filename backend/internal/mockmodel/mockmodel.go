// Package mockmodel is the reference implementation of spec/inference-api.md.
//
// It serves a full capability manifest and completes jobs with real artifacts:
// procedural PNGs (image modality) or an embedded canned MP4 (video modality),
// uploaded to the request's presigned PUT URLs with sha256 reported — so the
// orchestrator's artifact-landing path and the conformance suite exercise the
// full contract. All of Iris is developed against this until real R&D
// endpoints land; backend/internal/conformance drives it in CI and is the
// suite R&D runs against their implementations.
//
// Failure injection (orchestrator retry/error-path testing):
//
//	prompt containing "FAIL:safety"    -> safety_blocked (not retryable)
//	prompt containing "FAIL:transient" -> transient on the FIRST job with that
//	                                      exact prompt; a retry (new job id,
//	                                      same prompt — the orchestrator's
//	                                      retry shape) succeeds
//	prompt containing "SLOW"           -> 60s runtime (cancel testing)
package mockmodel

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"strings"
	"sync"
	"time"
)

//go:embed canned.mp4
var cannedMP4 []byte

// Options tune the mock for tests; zero value = production-ish defaults.
type Options struct {
	Modality string        // "video" (default) or "image"
	RunFor   time.Duration // simulated generation time (default 3s)
}

type server struct {
	opts       Options
	mu         sync.Mutex
	jobs       map[string]*job
	failedOnce map[string]bool // FAIL:transient bookkeeping, by exact prompt
	client     *http.Client
}

type job struct {
	ID        string     `json:"id"`
	State     string     `json:"state"` // queued|running|uploading|complete|failed|canceled
	Progress  float64    `json:"progress"`
	EtaS      *int       `json:"eta_s"`
	Artifacts []artifact `json:"artifacts,omitempty"`
	Error     *jobError  `json:"error,omitempty"`
	Metrics   *metrics   `json:"metrics,omitempty"`
}

type artifact struct {
	Index       int     `json:"index"`
	ContentType string  `json:"content_type"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	DurationS   float64 `json:"duration_s,omitempty"`
	FPS         float64 `json:"fps,omitempty"`
	Uploaded    bool    `json:"uploaded"`
	SHA256      string  `json:"sha256"`
	Safety      struct {
		Flagged bool `json:"flagged"`
	} `json:"safety"`
}

type metrics struct {
	GPUSeconds float64 `json:"gpu_seconds"`
}

type jobError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type createRequest struct {
	ID     string `json:"id"`
	Task   string `json:"task"`
	Prompt string `json:"prompt"`
	Seed   int64  `json:"seed"`
	Output struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"output"`
	Upload struct {
		Artifacts []struct {
			PutURL      string `json:"put_url"`
			ContentType string `json:"content_type"`
		} `json:"artifacts"`
	} `json:"upload"`
}

// Handler returns the mock endpoint as an http.Handler (mountable under
// httptest for in-process conformance runs).
func Handler(opts Options) http.Handler {
	if opts.Modality == "" {
		opts.Modality = "video"
	}
	if opts.RunFor == 0 {
		opts.RunFor = 3 * time.Second
	}
	s := &server{
		opts:       opts,
		jobs:       map[string]*job{},
		failedOnce: map[string]bool{},
		client:     &http.Client{Timeout: 30 * time.Second},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("GET /v1/manifest", s.getManifest)
	mux.HandleFunc("POST /v1/jobs", s.createJob)
	mux.HandleFunc("GET /v1/jobs/{id}", s.getJob)
	mux.HandleFunc("DELETE /v1/jobs/{id}", s.cancelJob)
	return auth(mux)
}

func auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			writeJSON(w, 401, errBody("invalid_input", "missing bearer token", false))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) createJob(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, errBody("invalid_input", "bad json", false))
		return
	}
	if req.ID == "" {
		writeJSON(w, 400, errBody("invalid_input", "missing id", false))
		return
	}

	s.mu.Lock()
	if j, ok := s.jobs[req.ID]; ok { // idempotent create
		s.mu.Unlock()
		writeJSON(w, 200, j)
		return
	}
	j := &job{ID: req.ID, State: "queued"}
	s.jobs[req.ID] = j
	s.mu.Unlock()

	go s.run(j, req)
	writeJSON(w, 202, map[string]any{"id": j.ID, "state": "queued", "queue_position": 0})
}

func (s *server) run(j *job, req createRequest) {
	time.Sleep(100 * time.Millisecond)
	if !s.transition(j, "queued", "running") {
		return // canceled while queued
	}

	switch {
	case strings.Contains(req.Prompt, "FAIL:safety"):
		s.fail(j, "safety_blocked", "prompt rejected by mock policy", false)
		return
	case strings.Contains(req.Prompt, "FAIL:transient"):
		s.mu.Lock()
		first := !s.failedOnce[req.Prompt]
		s.failedOnce[req.Prompt] = true
		s.mu.Unlock()
		if first {
			s.fail(j, "transient", "mock transient failure (retry with same prompt succeeds)", true)
			return
		}
	}

	runFor := s.opts.RunFor
	if strings.Contains(req.Prompt, "SLOW") {
		runFor = 60 * time.Second
	}
	// Sleep in slices so cancellation is responsive.
	deadline := time.Now().Add(runFor)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		s.mu.Lock()
		canceled := j.State == "canceled"
		if !canceled {
			total := runFor.Seconds()
			j.Progress = min(0.95, 1-time.Until(deadline).Seconds()/total)
		}
		s.mu.Unlock()
		if canceled {
			return
		}
	}

	if !s.transition(j, "running", "uploading") {
		return
	}
	art, err := s.makeAndUploadArtifact(req)
	if err != nil {
		s.fail(j, "internal", fmt.Sprintf("artifact upload: %v", err), true)
		return
	}
	s.mu.Lock()
	j.Artifacts = []artifact{art}
	j.State = "complete"
	j.Progress = 1
	j.Metrics = &metrics{GPUSeconds: runFor.Seconds() * 10}
	s.mu.Unlock()
}

// makeAndUploadArtifact produces real bytes and PUTs them to the request's
// presigned target when one is provided; otherwise the artifact is reported
// with uploaded=false (spec allows callers that only probe lifecycle).
func (s *server) makeAndUploadArtifact(req createRequest) (artifact, error) {
	var (
		data []byte
		art  artifact
	)
	if s.opts.Modality == "image" {
		w, h := req.Output.Width, req.Output.Height
		if w == 0 || h == 0 {
			w, h = 512, 512
		}
		data = proceduralPNG(w, h, req.Prompt, req.Seed)
		art = artifact{ContentType: "image/png", Width: w, Height: h}
	} else {
		data = cannedMP4
		art = artifact{ContentType: "video/mp4", Width: 640, Height: 360, DurationS: 2, FPS: 24}
	}
	sum := sha256.Sum256(data)
	art.SHA256 = hex.EncodeToString(sum[:])

	if len(req.Upload.Artifacts) > 0 && req.Upload.Artifacts[0].PutURL != "" {
		put, err := http.NewRequest(http.MethodPut, req.Upload.Artifacts[0].PutURL, bytes.NewReader(data))
		if err != nil {
			return art, err
		}
		put.Header.Set("Content-Type", art.ContentType)
		resp, err := s.client.Do(put)
		if err != nil {
			return art, err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			return art, fmt.Errorf("PUT returned %d", resp.StatusCode)
		}
		art.Uploaded = true
	}
	return art, nil
}

// proceduralPNG renders a deterministic two-tone gradient keyed by prompt+seed,
// so different recipes are visually distinguishable in the dev UI.
func proceduralPNG(w, h int, prompt string, seed int64) []byte {
	hsh := fnv.New32a()
	fmt.Fprintf(hsh, "%s|%d", prompt, seed)
	k := hsh.Sum32()
	c1 := color.RGBA{uint8(k), uint8(k >> 8), uint8(k >> 16), 255}
	c2 := color.RGBA{uint8(k >> 16), uint8(k), uint8(k >> 8), 255}

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		t := float64(y) / float64(h)
		row := color.RGBA{
			uint8(float64(c1.R)*(1-t) + float64(c2.R)*t),
			uint8(float64(c1.G)*(1-t) + float64(c2.G)*t),
			uint8(float64(c1.B)*(1-t) + float64(c2.B)*t),
			255,
		}
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, row)
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func (s *server) getJob(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	j, ok := s.jobs[r.PathValue("id")]
	s.mu.Unlock()
	if !ok {
		writeJSON(w, 404, errBody("invalid_input", "no such job", false))
		return
	}
	writeJSON(w, 200, j)
}

func (s *server) cancelJob(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if j, ok := s.jobs[r.PathValue("id")]; ok {
		switch j.State {
		case "complete", "failed", "canceled":
		default:
			j.State = "canceled"
		}
	}
	s.mu.Unlock()
	w.WriteHeader(204)
}

func (s *server) transition(j *job, from, to string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j.State != from {
		return false
	}
	j.State = to
	return true
}

func (s *server) fail(j *job, code, msg string, retryable bool) {
	s.mu.Lock()
	j.State = "failed"
	j.Error = &jobError{Code: code, Message: msg, Retryable: retryable}
	s.mu.Unlock()
}

func (s *server) getManifest(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(Manifest(s.opts.Modality))
}

// Manifest returns the capability manifest document for the given modality.
// The conformance suite validates it against spec/manifest.schema.json.
func Manifest(modality string) []byte {
	m := map[string]any{
		"spec_version": "1.0",
		"id":           "mock-" + modality,
		"family":       "mock",
		"version":      "0.1.0",
		"modality":     modality,
		"tasks":        []string{"t2v", "i2v", "v2v", "extend"},
		"profiles": map[string]any{
			"draft":  map[string]any{"max_width": 832, "max_height": 480, "steps": 12},
			"master": map[string]any{"max_width": 1920, "max_height": 1080, "steps": 40},
		},
		"duration": map[string]any{"min_s": 2, "max_s": 12, "fps": []int{24}},
		"references": map[string]any{
			"image": map[string]any{"max": 4, "roles": []string{"character", "style", "scene_view"}, "weighted": true},
			"audio": map[string]any{"max": 1, "roles": []string{"speech_lipsync", "music"}},
		},
		"conditioning": map[string]any{
			"first_frame": true, "last_frame": true,
			"keyframes":      map[string]any{"max": 5},
			"depth_sequence": map[string]any{"formats": []string{"png16.zip"}},
			"source_video":   true, "mask": true,
		},
		"features": map[string]any{
			"seed": true, "negative_prompt": true,
			"lip_sync_in_gen": false, "lip_sync_post": true,
			"audio_gen": false, "camera_control": "depth_path",
			"v2v_restyle": true, "video_inpaint": "mask+prompt",
		},
		"params_schema": map[string]any{"type": "object", "properties": map[string]any{
			"guidance": map[string]any{"type": "number", "minimum": 1, "maximum": 20, "default": 7},
		}},
		"pricing": map[string]any{"unit": "gpu_second", "estimates": map[string]any{"draft": 0.4, "master": 3.1}},
		"limits":  map[string]any{"concurrency": 8, "max_queue": 64},
	}
	if modality == "image" {
		m["tasks"] = []string{"t2i", "i2i", "inpaint", "outpaint", "upscale"}
		delete(m, "duration")
	}
	b, _ := json.Marshal(m)
	return b
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func errBody(code, msg string, retryable bool) map[string]any {
	return map[string]any{"error": jobError{Code: code, Message: msg, Retryable: retryable}}
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
