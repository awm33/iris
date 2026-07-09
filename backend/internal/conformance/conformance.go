// Package conformance checks a model endpoint against spec/inference-api.md.
//
// It is the definition of "works in Iris": backend/internal/mockmodel passes
// it in CI, and R&D runs it (via cmd/conformance) against real Wan/Qwen
// endpoints before integration. Checks marked FailureInjection exercise the
// spec's error taxonomy via the mock's magic prompts and are skipped against
// real endpoints unless they implement the same injection hooks.
package conformance

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/awm33/iris/backend/internal/inference"
)

type Config struct {
	BaseURL          string
	Token            string
	FailureInjection bool          // run the FAIL:*/SLOW magic-prompt checks (mock, or endpoints implementing them)
	PollInterval     time.Duration // default 250ms
	Timeout          time.Duration // per-check; default 120s
	// ReceiverHost: hostname the endpoint under test uses to reach this
	// machine's artifact receiver (e.g. "host.docker.internal" for a
	// dockerized endpoint). Empty = loopback (in-process/native endpoints).
	ReceiverHost string
}

type Result struct {
	Name    string
	Err     error // nil = pass
	Skipped bool
	Detail  string
}

// Run executes every check and returns one Result per check. It never panics;
// a failing endpoint yields failing Results.
func Run(ctx context.Context, cfg Config) []Result {
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 250 * time.Millisecond
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 120 * time.Second
	}
	c := &checker{cfg: cfg, http: &http.Client{Timeout: 30 * time.Second}}

	checks := []struct {
		name string
		skip bool
		fn   func(context.Context) (string, error)
	}{
		{"healthz", false, c.checkHealthz},
		{"auth_required", false, c.checkAuthRequired},
		{"manifest_valid", false, c.checkManifest},
		{"job_lifecycle_and_artifact", false, c.checkLifecycle},
		{"idempotent_create", false, c.checkIdempotency},
		{"mask_semantics", false, c.checkMaskSemantics}, // self-skips when inpaint undeclared
		{"cancel", !cfg.FailureInjection, c.checkCancel}, // needs SLOW injection to be reliable
		{"error_safety_blocked", !cfg.FailureInjection, c.checkSafetyBlocked},
		{"error_transient_retryable", !cfg.FailureInjection, c.checkTransient},
	}

	var results []Result
	for _, ch := range checks {
		if ch.skip {
			results = append(results, Result{Name: ch.name, Skipped: true})
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
		detail, err := ch.fn(cctx)
		cancel()
		results = append(results, Result{Name: ch.name, Err: err, Detail: detail})
	}
	return results
}

type checker struct {
	cfg  Config
	http *http.Client
	man  *manifestLite // lazy manifest cache for job-body building
}

func (c *checker) url(p string) string { return strings.TrimSuffix(c.cfg.BaseURL, "/") + p }

func (c *checker) do(ctx context.Context, method, path string, body any, auth bool) (*http.Response, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url(path), rd)
	if err != nil {
		return nil, nil, err
	}
	if auth {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp, data, err
}

func (c *checker) checkHealthz(ctx context.Context) (string, error) {
	resp, _, err := c.do(ctx, "GET", "/v1/healthz", nil, true)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("want 200, got %d", resp.StatusCode)
	}
	return "", nil
}

func (c *checker) checkAuthRequired(ctx context.Context) (string, error) {
	resp, _, err := c.do(ctx, "GET", "/v1/manifest", nil, false)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 401 {
		return "", fmt.Errorf("unauthenticated request: want 401, got %d", resp.StatusCode)
	}
	return "", nil
}

func (c *checker) checkManifest(ctx context.Context) (string, error) {
	resp, data, err := c.do(ctx, "GET", "/v1/manifest", nil, true)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("want 200, got %d", resp.StatusCode)
	}
	if err := inference.ValidateManifestDocument(data); err != nil {
		return "", err
	}
	var m struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(data, &m)
	return "id=" + m.ID, nil
}

// artifactReceiver is a local HTTP server standing in for presigned PUT
// targets. The endpoint under test must be able to reach it (same host for
// the mock; a tunnel may be needed for remote endpoints).
type artifactReceiver struct {
	srv           *httptest.Server
	advertiseHost string
	mu            sync.Mutex
	got           map[string][]byte // path -> body (PUT targets)
	inputs        map[string][]byte // path -> body served on GET (conditioning inputs)
}

// base rewrites the listener URL to the advertised host when set.
func (r *artifactReceiver) base() string {
	if r.advertiseHost == "" {
		return r.srv.URL
	}
	u, _ := url.Parse(r.srv.URL)
	return "http://" + net.JoinHostPort(r.advertiseHost, u.Port())
}

// newArtifactReceiver binds loopback-only when advertiseHost is empty;
// otherwise it listens on all interfaces and advertises URLs at
// advertiseHost so a containerized endpoint can reach it.
func newArtifactReceiver(advertiseHost string) *artifactReceiver {
	r := &artifactReceiver{got: map[string][]byte{}, inputs: map[string][]byte{}, advertiseHost: advertiseHost}
	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodPut:
			body, _ := io.ReadAll(io.LimitReader(req.Body, 256<<20))
			r.mu.Lock()
			r.got[req.URL.Path] = body
			r.mu.Unlock()
			w.WriteHeader(200)
		case http.MethodGet:
			r.mu.Lock()
			body, ok := r.inputs[req.URL.Path]
			r.mu.Unlock()
			if !ok {
				w.WriteHeader(404)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(body)
		default:
			w.WriteHeader(405)
		}
	})
	if advertiseHost == "" {
		r.srv = httptest.NewServer(handler)
	} else {
		ln, err := net.Listen("tcp", "0.0.0.0:0")
		if err != nil {
			panic(err) // dev tool; same failure mode as httptest
		}
		r.srv = &httptest.Server{Listener: ln, Config: &http.Server{Handler: handler}}
		r.srv.Start()
	}
	return r
}

// serveInput hosts conditioning bytes at path (stand-in for a signed GET URL).
func (r *artifactReceiver) serveInput(path string, body []byte) string {
	r.mu.Lock()
	r.inputs[path] = body
	r.mu.Unlock()
	return r.base() + path
}

func (r *artifactReceiver) putURL(path string) string { return r.base() + path }
func (r *artifactReceiver) body(path string) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.got[path]
}
func (r *artifactReceiver) Close() { r.srv.Close() }

// manifestLite is the slice of the manifest the suite needs to build jobs an
// endpoint actually declares — a removal-only inpainter must not be asked
// for t2v (spec: undeclared capabilities are rejected).
type manifestLite struct {
	Tasks        []string `json:"tasks"`
	Conditioning struct {
		Mask        bool `json:"mask"`
		SourceImage bool `json:"source_image"`
	} `json:"conditioning"`
	Features struct {
		Prompt *bool `json:"prompt"`
	} `json:"features"`
}

func (m *manifestLite) has(task string) bool {
	for _, t := range m.Tasks {
		if t == task {
			return true
		}
	}
	return false
}
func (m *manifestLite) promptOK() bool { return m.Features.Prompt == nil || *m.Features.Prompt }

func (c *checker) manifestLite(ctx context.Context) (*manifestLite, error) {
	if c.man != nil {
		return c.man, nil
	}
	_, data, err := c.do(ctx, "GET", "/v1/manifest", nil, true)
	if err != nil {
		return nil, err
	}
	m := &manifestLite{}
	if err := json.Unmarshal(data, m); err != nil {
		return nil, fmt.Errorf("manifest unparseable: %w", err)
	}
	c.man = m
	return m, nil
}

// buildJobBody assembles a create-job request for a task the endpoint
// declares (preferring generative tasks, falling back to inpaint with
// conditioning fixtures served from recv). Prompt is dropped for
// prompt-ignoring specialists.
func (c *checker) buildJobBody(ctx context.Context, id, prompt string, recv *artifactReceiver) (map[string]any, error) {
	m, err := c.manifestLite(ctx)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"id": id, "profile": "draft"}
	if prompt != "" && m.promptOK() {
		body["prompt"] = prompt
	}
	switch {
	case m.has("t2v"):
		body["task"] = "t2v"
		body["output"] = map[string]any{"width": 640, "height": 360, "duration_s": 2, "fps": 24}
	case m.has("t2i"):
		body["task"] = "t2i"
		body["output"] = map[string]any{"width": 512, "height": 512}
	case m.has("inpaint") && m.Conditioning.Mask && m.Conditioning.SourceImage:
		if recv == nil {
			return nil, fmt.Errorf("inpaint-only endpoint needs conditioning fixtures for this check")
		}
		src, mask := tinyInpaintFixture()
		body["task"] = "inpaint"
		body["output"] = map[string]any{"width": 64, "height": 64}
		body["conditioning"] = map[string]any{
			"source_image": map[string]any{"url": recv.serveInput("/inputs/"+id+"-src.png", src)},
			"mask":         map[string]any{"url": recv.serveInput("/inputs/"+id+"-mask.png", mask)},
		}
	default:
		return nil, fmt.Errorf("no task this suite can drive (tasks=%v)", m.Tasks)
	}
	return body, nil
}

// tinyInpaintFixture: flat background, red object inside the white-masked
// center square (an object to remove — see checkMaskSemantics).
func tinyInpaintFixture() (src, mask []byte) {
	const dim, lo, hi = 64, 16, 48
	s := image.NewRGBA(image.Rect(0, 0, dim, dim))
	draw.Draw(s, s.Bounds(), &image.Uniform{color.RGBA{90, 120, 150, 255}}, image.Point{}, draw.Src)
	draw.Draw(s, image.Rect(lo+2, lo+2, hi-2, hi-2), &image.Uniform{color.RGBA{200, 40, 40, 255}}, image.Point{}, draw.Src)
	m := image.NewGray(image.Rect(0, 0, dim, dim))
	for y := lo; y < hi; y++ {
		for x := lo; x < hi; x++ {
			m.SetGray(x, y, color.Gray{255})
		}
	}
	return encodePNG(s), encodePNG(m)
}

func (c *checker) createAndPoll(ctx context.Context, id, prompt string, recv *artifactReceiver, artifactPath string) (map[string]any, error) {
	body, err := c.buildJobBody(ctx, id, prompt, recv)
	if err != nil {
		return nil, err
	}
	if recv != nil && artifactPath != "" {
		body["upload"] = map[string]any{"artifacts": []map[string]any{{"put_url": recv.putURL(artifactPath), "content_type": "application/octet-stream"}}}
	}
	resp, data, err := c.do(ctx, "POST", "/v1/jobs", body, true)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 202 && resp.StatusCode != 200 {
		return nil, fmt.Errorf("create: want 202/200, got %d: %s", resp.StatusCode, data)
	}
	return c.poll(ctx, id)
}

// poll waits for the job to reach a terminal state.
func (c *checker) poll(ctx context.Context, id string) (map[string]any, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("timed out polling job %s", id)
		case <-time.After(c.cfg.PollInterval):
		}
		resp, data, err := c.do(ctx, "GET", "/v1/jobs/"+id, nil, true)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("status: want 200, got %d", resp.StatusCode)
		}
		var j map[string]any
		if err := json.Unmarshal(data, &j); err != nil {
			return nil, err
		}
		switch j["state"] {
		case "complete", "failed", "canceled":
			return j, nil
		}
	}
}

func (c *checker) checkLifecycle(ctx context.Context) (string, error) {
	recv := newArtifactReceiver(c.cfg.ReceiverHost)
	defer recv.Close()

	id := fmt.Sprintf("j_conf_%d", time.Now().UnixNano())
	j, err := c.createAndPoll(ctx, id, "conformance lifecycle", recv, "/a0")
	if err != nil {
		return "", err
	}
	if j["state"] != "complete" {
		return "", fmt.Errorf("want complete, got %v (error: %v)", j["state"], j["error"])
	}
	arts, _ := j["artifacts"].([]any)
	if len(arts) == 0 {
		return "", fmt.Errorf("complete job reports no artifacts")
	}
	a0, _ := arts[0].(map[string]any)
	if up, _ := a0["uploaded"].(bool); !up {
		return "", fmt.Errorf("artifact not uploaded")
	}
	body := recv.body("/a0")
	if len(body) == 0 {
		return "", fmt.Errorf("no bytes received at presigned PUT target")
	}
	sum := sha256.Sum256(body)
	if got := a0["sha256"]; got != hex.EncodeToString(sum[:]) {
		return "", fmt.Errorf("sha256 mismatch: reported %v, received %s", got, hex.EncodeToString(sum[:]))
	}
	return fmt.Sprintf("artifact %d bytes, sha256 verified", len(body)), nil
}

func (c *checker) checkIdempotency(ctx context.Context) (string, error) {
	id := fmt.Sprintf("j_conf_idem_%d", time.Now().UnixNano())
	recv := newArtifactReceiver(c.cfg.ReceiverHost)
	defer recv.Close()
	body, err := c.buildJobBody(ctx, id, "idempotency check", recv)
	if err != nil {
		return "", err
	}
	resp1, _, err := c.do(ctx, "POST", "/v1/jobs", body, true)
	if err != nil {
		return "", err
	}
	resp2, _, err := c.do(ctx, "POST", "/v1/jobs", body, true)
	if err != nil {
		return "", err
	}
	if resp1.StatusCode != 202 {
		return "", fmt.Errorf("first create: want 202, got %d", resp1.StatusCode)
	}
	if resp2.StatusCode != 200 {
		return "", fmt.Errorf("repeat create: want 200 (same job), got %d", resp2.StatusCode)
	}
	return "", nil
}

// checkMaskSemantics enforces the inpaint contract (spec §2): white mask
// regions are generated, black regions are preserved BYTE-FAITHFULLY (±2 per
// 8-bit channel for codec round-trips). Runs only when the manifest declares
// inpaint + mask + source_image.
func (c *checker) checkMaskSemantics(ctx context.Context) (string, error) {
	m, err := c.manifestLite(ctx)
	if err != nil {
		return "", err
	}
	if !m.has("inpaint") || !m.Conditioning.Mask || !m.Conditioning.SourceImage {
		return "inpaint/mask/source_image not declared — nothing to check", nil
	}

	recv := newArtifactReceiver(c.cfg.ReceiverHost)
	defer recv.Close()

	// Fixture: flat background with a red "object" inside the white mask
	// region. The object matters for the removal assertion — on a
	// featureless source, a PERFECT background reconstruction is
	// byte-identical to the input and would read as "unchanged".
	const dim, lo, hi = 64, 16, 48
	srcPNG, maskPNG := tinyInpaintFixture()
	src, _, err := image.Decode(bytes.NewReader(srcPNG))
	if err != nil {
		return "", err
	}
	srcURL := recv.serveInput("/inputs/source.png", srcPNG)
	maskURL := recv.serveInput("/inputs/mask.png", maskPNG)
	near := func(a, b uint32) bool { d := int(a>>8) - int(b>>8); return d >= -2 && d <= 2 }
	cx := (lo + hi) / 2

	runOne := func(id, prompt, artifactPath string) (image.Image, error) {
		body := map[string]any{
			"id": id, "task": "inpaint", "profile": "draft",
			"output": map[string]any{"width": dim, "height": dim},
			"conditioning": map[string]any{
				"source_image": map[string]any{"url": srcURL},
				"mask":         map[string]any{"url": maskURL},
			},
			"upload": map[string]any{"artifacts": []map[string]any{{"put_url": recv.putURL(artifactPath), "content_type": "image/png"}}},
		}
		if prompt != "" {
			body["prompt"] = prompt
		}
		resp, data, err := c.do(ctx, "POST", "/v1/jobs", body, true)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != 202 && resp.StatusCode != 200 {
			return nil, fmt.Errorf("create: want 202/200, got %d: %s", resp.StatusCode, data)
		}
		j, err := c.poll(ctx, id)
		if err != nil {
			return nil, err
		}
		if j["state"] != "complete" {
			return nil, fmt.Errorf("want complete, got %v (error: %v)", j["state"], j["error"])
		}
		out, _, err := image.Decode(bytes.NewReader(recv.body(artifactPath)))
		if err != nil {
			return nil, fmt.Errorf("artifact does not decode as an image: %w", err)
		}
		if !out.Bounds().Eq(src.Bounds()) {
			return nil, fmt.Errorf("artifact bounds %v != source %v", out.Bounds(), src.Bounds())
		}
		return out, nil
	}
	assertMask := func(out image.Image, label string) error {
		for _, p := range [][2]int{{4, 4}, {dim - 4, 4}, {4, dim - 4}, {dim - 4, dim - 4}} {
			r1, g1, b1, _ := src.At(p[0], p[1]).RGBA()
			r2, g2, b2, _ := out.At(p[0], p[1]).RGBA()
			if !near(r1, r2) || !near(g1, g2) || !near(b1, b2) {
				return fmt.Errorf("%s: black-mask pixel at %v was modified — masks must preserve unselected regions", label, p)
			}
		}
		r1, g1, b1, _ := src.At(cx, cx).RGBA()
		r2, g2, b2, _ := out.At(cx, cx).RGBA()
		if near(r1, r2) && near(g1, g2) && near(b1, b2) {
			return fmt.Errorf("%s: white-mask pixel at (%d,%d) unchanged — masked region was not generated", label, cx, cx)
		}
		return nil
	}

	// Prompted generation variant — only for endpoints that condition on
	// prompts (a removal specialist rejects prompted jobs by contract).
	detail := "removal"
	if m.promptOK() {
		id := fmt.Sprintf("j_conf_mask_%d", time.Now().UnixNano())
		out, err := runOne(id, "conformance mask semantics", "/a0")
		if err != nil {
			return "", err
		}
		if err := assertMask(out, "prompted"); err != nil {
			return "", err
		}
		detail = "generation + removal"
	}

	// Removal contract (spec §2): an inpaint job with the prompt OMITTED must
	// be accepted and obey the same mask semantics — background reconstructed
	// in the white region, black region untouched. (What fills the region is
	// the model's business; that it changes and preserves is the contract.)
	rid := fmt.Sprintf("j_conf_removal_%d", time.Now().UnixNano())
	rout, err := runOne(rid, "", "/a1")
	if err != nil {
		return "", fmt.Errorf("removal (no prompt): %w", err)
	}
	if err := assertMask(rout, "removal"); err != nil {
		return "", err
	}
	return detail + ": black regions preserved, white regions changed", nil
}

func encodePNG(img image.Image) []byte {
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func (c *checker) checkCancel(ctx context.Context) (string, error) {
	id := fmt.Sprintf("j_conf_cancel_%d", time.Now().UnixNano())
	recv := newArtifactReceiver(c.cfg.ReceiverHost)
	defer recv.Close()
	body, err := c.buildJobBody(ctx, id, "SLOW conformance cancel", recv)
	if err != nil {
		return "", err
	}
	if resp, data, err := c.do(ctx, "POST", "/v1/jobs", body, true); err != nil {
		return "", err
	} else if resp.StatusCode != 202 {
		return "", fmt.Errorf("create: want 202, got %d: %s", resp.StatusCode, data)
	}
	time.Sleep(300 * time.Millisecond) // let it reach running
	if resp, _, err := c.do(ctx, "DELETE", "/v1/jobs/"+id, nil, true); err != nil {
		return "", err
	} else if resp.StatusCode != 204 {
		return "", fmt.Errorf("cancel: want 204, got %d", resp.StatusCode)
	}
	// poll until terminal
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, data, err := c.do(ctx, "GET", "/v1/jobs/"+id, nil, true)
		if err != nil {
			return "", err
		}
		var j map[string]any
		_ = json.Unmarshal(data, &j)
		if j["state"] == "canceled" {
			return "", nil
		}
		if s := j["state"]; s == "complete" || s == "failed" {
			return "", fmt.Errorf("want canceled, got %v", s)
		}
		time.Sleep(c.cfg.PollInterval)
	}
	return "", fmt.Errorf("job never reached canceled")
}

func (c *checker) checkSafetyBlocked(ctx context.Context) (string, error) {
	id := fmt.Sprintf("j_conf_safety_%d", time.Now().UnixNano())
	j, err := c.createAndPoll(ctx, id, "FAIL:safety conformance", nil, "")
	if err != nil {
		return "", err
	}
	if j["state"] != "failed" {
		return "", fmt.Errorf("want failed, got %v", j["state"])
	}
	e, _ := j["error"].(map[string]any)
	if e == nil || e["code"] != "safety_blocked" {
		return "", fmt.Errorf("want error.code=safety_blocked, got %v", e)
	}
	if r, _ := e["retryable"].(bool); r {
		return "", fmt.Errorf("safety_blocked must not be retryable")
	}
	return "", nil
}

func (c *checker) checkTransient(ctx context.Context) (string, error) {
	// Unique prompt per run so the check is repeatable against long-lived
	// endpoints (the injection keys transient failure on the exact prompt).
	id := fmt.Sprintf("j_conf_trans_%d", time.Now().UnixNano())
	prompt := "FAIL:transient conformance " + id
	j, err := c.createAndPoll(ctx, id, prompt, nil, "")
	if err != nil {
		return "", err
	}
	e, _ := j["error"].(map[string]any)
	if j["state"] != "failed" || e == nil || e["code"] != "transient" {
		return "", fmt.Errorf("want failed/transient, got state=%v error=%v", j["state"], e)
	}
	if r, _ := e["retryable"].(bool); !r {
		return "", fmt.Errorf("transient must be retryable")
	}
	// Orchestrator retry shape: NEW job id, same request. Must succeed.
	j2, err := c.createAndPoll(ctx, id+"_retry", prompt, nil, "")
	if err != nil {
		return "", err
	}
	if j2["state"] != "complete" {
		return "", fmt.Errorf("retry: want complete, got %v (error: %v)", j2["state"], j2["error"])
	}
	return "", nil
}
