// Package mockbfl is a recorded-shape mock of the Black Forest Labs FLUX
// API (shapes from api.bfl.ai/openapi.json, 2026-07-10) — enough surface
// for the bfl adapter to run its full lifecycle against localhost with
// zero real-remote traffic: submit (t2i + fill) → {id, polling_url} →
// poll → Ready → short-lived sample URL. Content is deterministic
// (seeded), and fill actually composites the caller's mask so gen-fill
// plumbing is visually verifiable end to end.
//
// Recorded, not gospel: field names and enums must be VERIFIED against
// the live API before real keys (standing discipline).
package mockbfl

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/http"
	"strings"
	"sync"
	"time"
)

type task struct {
	ReadyAt   time.Time
	Moderated bool
	PNG       []byte
	Polls     int
}

type Server struct {
	Key string

	mu    sync.Mutex
	tasks map[string]*task
	next  int
}

func New(key string) *Server {
	return &Server{Key: key, tasks: map[string]*task{}}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/ping", s.auth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	mux.HandleFunc("POST /v1/flux-2-pro", s.auth(s.submitT2I))
	mux.HandleFunc("POST /v1/flux-pro-1.0-fill", s.auth(s.submitFill))
	mux.HandleFunc("GET /v1/poll/{id}", s.auth(s.poll))
	// Sample delivery deliberately does NOT require the key — the real
	// delivery URLs are signed standalone links on a separate host.
	mux.HandleFunc("GET /v1/sample/{id}", s.sample)
	return mux
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-key") != s.Key {
			http.Error(w, `{"detail":"invalid key"}`, http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) register(t *task) (id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	id = fmt.Sprintf("bfl-task-%d", s.next)
	s.tasks[id] = t
	return id
}

func (s *Server) respondCreated(w http.ResponseWriter, r *http.Request, id string) {
	base := "http://" + r.Host
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":          id,
		"polling_url": base + "/v1/poll/" + id,
	})
}

// t2iField matches exactly the recorded request surface: a misspelled or
// invented adapter field must 422 here, or the mock's shape-lock is
// theater (review PR40-L2).
func t2iFieldOK(k string) bool {
	switch k {
	case "prompt", "seed", "width", "height", "output_format", "input_image":
		return true
	}
	if n, ok := strings.CutPrefix(k, "input_image_"); ok {
		i := 0
		_, err := fmt.Sscanf(n, "%d", &i)
		return err == nil && i >= 2 && i <= 10
	}
	return false
}

func (s *Server) submitT2I(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Prompt string `json:"prompt"`
		Seed   int64  `json:"seed"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	}
	raw := map[string]json.RawMessage{}
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		http.Error(w, `{"detail":"bad json"}`, http.StatusBadRequest)
		return
	}
	for k := range raw {
		if !t2iFieldOK(k) {
			http.Error(w, fmt.Sprintf(`{"detail":"unknown field %q"}`, k), http.StatusUnprocessableEntity)
			return
		}
	}
	get := func(k string, v any) { _ = json.Unmarshal(raw[k], v) }
	get("prompt", &body.Prompt)
	get("seed", &body.Seed)
	get("width", &body.Width)
	get("height", &body.Height)
	if strings.TrimSpace(body.Prompt) == "" {
		http.Error(w, `{"detail":"prompt required"}`, http.StatusUnprocessableEntity)
		return
	}
	w0, h0 := body.Width, body.Height
	if w0 <= 0 || w0 > 4096 {
		w0 = 1024
	}
	if h0 <= 0 || h0 > 4096 {
		h0 = 1024
	}
	// References tint the output so multi-ref plumbing is visible.
	refs := 0
	for k := range raw {
		if k == "input_image" || strings.HasPrefix(k, "input_image_") {
			refs++
		}
	}
	t := &task{
		ReadyAt:   time.Now().Add(1200 * time.Millisecond),
		Moderated: strings.Contains(strings.ToLower(body.Prompt), "unsafe"),
		PNG:       gradientPNG(body.Prompt, body.Seed+int64(refs)*17, w0, h0),
	}
	s.respondCreated(w, r, s.register(t))
}

func (s *Server) submitFill(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Image        string `json:"image"`
		Mask         string `json:"mask"`
		Prompt       string `json:"prompt"`
		Seed         int64  `json:"seed"`
		OutputFormat string `json:"output_format"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields() // shape-lock: invented fields must 422
	if err := dec.Decode(&body); err != nil {
		http.Error(w, `{"detail":"bad json or unknown field"}`, http.StatusUnprocessableEntity)
		return
	}
	src, err1 := decodePNGb64(body.Image)
	mask, err2 := decodePNGb64(body.Mask)
	if err1 != nil || err2 != nil {
		http.Error(w, `{"detail":"image and mask must be base64 images"}`, http.StatusUnprocessableEntity)
		return
	}
	// Dimension validation is part of the recorded contract.
	if !src.Bounds().Size().Eq(mask.Bounds().Size()) {
		http.Error(w, `{"detail":"mask dimensions must match image"}`, http.StatusUnprocessableEntity)
		return
	}
	t := &task{
		ReadyAt:   time.Now().Add(1200 * time.Millisecond),
		Moderated: strings.Contains(strings.ToLower(body.Prompt), "unsafe"),
		PNG:       fillPNG(src, mask, body.Prompt, body.Seed),
	}
	s.respondCreated(w, r, s.register(t))
}

func (s *Server) poll(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	t := s.tasks[id]
	if t != nil {
		t.Polls++
	}
	s.mu.Unlock()
	if t == nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "status": "Task not found"})
		return
	}
	switch {
	case t.Moderated:
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "status": "Content Moderated"})
	case time.Now().Before(t.ReadyAt):
		status := "Pending"
		if t.Polls > 1 {
			status = "Generating"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "status": status})
	default:
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":     id,
			"status": "Ready",
			"result": map[string]any{"sample": "http://" + r.Host + "/v1/sample/" + id},
		})
	}
}

func (s *Server) sample(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	t := s.tasks[id]
	s.mu.Unlock()
	if t == nil || time.Now().Before(t.ReadyAt) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(t.PNG)
}

func decodePNGb64(b64 string) (image.Image, error) {
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}

// gradientPNG: deterministic two-color vertical gradient from (prompt, seed)
// — same trick as mockmodel, so takes are visually distinguishable.
func gradientPNG(prompt string, seed int64, w, h int) []byte {
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

// fillPNG composites: unmasked pixels preserved from the source, masked
// (white) pixels replaced with the seeded gradient — gen-fill plumbing is
// verifiable at a glance.
func fillPNG(src, mask image.Image, prompt string, seed int64) []byte {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(out, out.Bounds(), src, b.Min, draw.Src)
	genData := gradientPNG(prompt, seed, w, h)
	gen, _, _ := image.Decode(bytes.NewReader(genData))
	mb := mask.Bounds()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bl, _ := mask.At(mb.Min.X+x, mb.Min.Y+y).RGBA()
			if (r+g+bl)/3 > 0x7fff { // white = inpaint (recorded semantics)
				out.Set(x, y, gen.At(x, y))
			}
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, out)
	return buf.Bytes()
}
