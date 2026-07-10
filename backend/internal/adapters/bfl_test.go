package adapters

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/awm33/iris/backend/internal/inference"
	"github.com/awm33/iris/backend/internal/mockbfl"
)

func asValidationError(err error, target **inference.ValidationError) bool {
	return errors.As(err, target)
}

// The adapter runs against the ACTUAL mock package — adapter and mock are
// shape-locked to each other (both recorded from the same OpenAPI snapshot;
// the live-key verification pass revalidates the pair together).

func bflPair(t *testing.T) (*bfl, *httptest.Server, *httptest.Server) {
	t.Helper()
	mock := httptest.NewServer(mockbfl.New("k").Handler())
	t.Cleanup(mock.Close)
	// Upload target + input host: accepts any PUT/serves inputs.
	store := map[string][]byte{}
	uploads := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			var buf bytes.Buffer
			_, _ = buf.ReadFrom(r.Body)
			store[r.URL.Path] = buf.Bytes()
			w.WriteHeader(http.StatusOK)
			return
		}
		if data, ok := store[r.URL.Path]; ok {
			_, _ = w.Write(data)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(uploads.Close)
	a := newBFL(mock.URL, "k")
	a.http.Timeout = 5 * time.Second
	return a, mock, uploads
}

func pngBytes(t *testing.T, w, h int, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func putFixture(t *testing.T, uploads *httptest.Server, path string, data []byte) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, uploads.URL+path, bytes.NewReader(data))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return uploads.URL + path
}

func pollUntilTerminal(t *testing.T, a *bfl, id string) (*inference.JobStatus, error) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		st, err := a.GetJob(context.Background(), id)
		if err != nil {
			return nil, err
		}
		if st.State == "complete" {
			return st, nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatal("poll timeout")
	return nil, nil
}

func TestBFLT2ILifecycle(t *testing.T) {
	a, _, uploads := bflPair(t)
	ref := putFixture(t, uploads, "/ref1.png", pngBytes(t, 8, 8, color.RGBA{255, 0, 0, 255}))
	st, err := a.CreateJob(context.Background(), &inference.CreateJobRequest{
		ID: "job-1", Task: "t2i", Prompt: "a red diner at dusk", Seed: 42,
		Output:     &inference.Output{Width: 64, Height: 48},
		References: []inference.Reference{{Kind: "image", Role: "character", URL: ref}},
		Upload: &inference.Upload{Artifacts: []inference.UploadTarget{{
			PutURL: uploads.URL + "/out.png", ContentType: "image/png",
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.State != "queued" {
		t.Fatalf("state = %s", st.State)
	}
	final, err := pollUntilTerminal(t, a, "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(final.Artifacts) != 1 || !final.Artifacts[0].Uploaded || final.Artifacts[0].SHA256 == "" {
		t.Fatalf("artifact not bridged: %+v", final.Artifacts)
	}
	// The upload host actually received a decodable PNG at the target dims.
	res, _ := http.Get(uploads.URL + "/out.png")
	img, _, err := image.Decode(res.Body)
	res.Body.Close()
	if err != nil {
		t.Fatalf("uploaded artifact not an image: %v", err)
	}
	if img.Bounds().Dx() != 64 || img.Bounds().Dy() != 48 {
		t.Fatalf("dims %v", img.Bounds())
	}
}

func TestBFLFillCompositesMask(t *testing.T) {
	a, _, uploads := bflPair(t)
	// Source: solid blue. Mask: white right half (white = inpaint).
	src := pngBytes(t, 16, 16, color.RGBA{0, 0, 255, 255})
	mask := func() []byte {
		img := image.NewRGBA(image.Rect(0, 0, 16, 16))
		for y := 0; y < 16; y++ {
			for x := 0; x < 16; x++ {
				if x >= 8 {
					img.Set(x, y, color.White)
				} else {
					img.Set(x, y, color.Black)
				}
			}
		}
		var buf bytes.Buffer
		_ = png.Encode(&buf, img)
		return buf.Bytes()
	}()
	srcURL := putFixture(t, uploads, "/src.png", src)
	maskURL := putFixture(t, uploads, "/mask.png", mask)
	_, err := a.CreateJob(context.Background(), &inference.CreateJobRequest{
		ID: "job-2", Task: "inpaint", Prompt: "fill with something",
		Conditioning: &inference.Conditioning{
			SourceImage: &inference.FrameRef{URL: srcURL},
			Mask:        &inference.FrameRef{URL: maskURL},
		},
		Upload: &inference.Upload{Artifacts: []inference.UploadTarget{{
			PutURL: uploads.URL + "/fill-out.png", ContentType: "image/png",
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pollUntilTerminal(t, a, "job-2"); err != nil {
		t.Fatal(err)
	}
	res, _ := http.Get(uploads.URL + "/fill-out.png")
	img, _, err := image.Decode(res.Body)
	res.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	// Unmasked half preserved (blue), masked half replaced (not blue).
	r0, g0, b0, _ := img.At(2, 8).RGBA()
	if !(b0 > 0xf000 && r0 < 0x1000 && g0 < 0x1000) {
		t.Fatalf("unmasked pixel not preserved: %v %v %v", r0, g0, b0)
	}
	r1, g1, b1, _ := img.At(13, 8).RGBA()
	if b1 > 0xf000 && r1 < 0x1000 && g1 < 0x1000 {
		t.Fatal("masked pixel not replaced")
	}
}

func TestBFLModerationMapsToSafetyBlocked(t *testing.T) {
	a, _, uploads := bflPair(t)
	_, err := a.CreateJob(context.Background(), &inference.CreateJobRequest{
		ID: "job-3", Task: "t2i", Prompt: "something unsafe here",
		Upload: &inference.Upload{Artifacts: []inference.UploadTarget{{PutURL: uploads.URL + "/x.png"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.GetJob(context.Background(), "job-3")
	if err != nil {
		t.Fatal(err)
	}
	// Task-terminal = STATUS-shaped failure (the orchestrator clears the
	// handle on these), never a Go error (those mean transport blips).
	if st.State != "failed" || st.Error == nil || st.Error.Code != "safety_blocked" || st.Error.Retryable {
		t.Fatalf("want failed status with terminal safety_blocked, got %+v", st)
	}
}

func TestBFLValidation(t *testing.T) {
	a, _, _ := bflPair(t)
	// inpaint without mask
	_, err := a.CreateJob(context.Background(), &inference.CreateJobRequest{
		ID: "j", Task: "inpaint", Prompt: "x",
		Conditioning: &inference.Conditioning{SourceImage: &inference.FrameRef{URL: "http://x/src.png"}},
	})
	var ve *inference.ValidationError
	if !asValidationError(err, &ve) {
		t.Fatalf("want validation error, got %v", err)
	}
	// unsupported task
	if _, err := a.CreateJob(context.Background(), &inference.CreateJobRequest{ID: "j2", Task: "t2v", Prompt: "x"}); err == nil {
		t.Fatal("t2v must be rejected")
	}
}

func TestBFLUnknownStatusKeepsRunning(t *testing.T) {
	// Open status set: a status we've never seen must NOT fail the job.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/flux-2-pro"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "t1", "polling_url": "http://" + r.Host + "/v1/poll/t1",
			})
		case strings.Contains(r.URL.Path, "/poll/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "t1", "status": "Queued Deep"})
		}
	}))
	defer srv.Close()
	a := newBFL(srv.URL, "k")
	if _, err := a.CreateJob(context.Background(), &inference.CreateJobRequest{ID: "j", Task: "t2i", Prompt: "x"}); err != nil {
		t.Fatal(err)
	}
	st, err := a.GetJob(context.Background(), "j")
	if err != nil || st.State != "running" {
		t.Fatalf("unknown status must keep running, got %v / %v", st, err)
	}
}

func TestBFLAuthFailure(t *testing.T) {
	mock := httptest.NewServer(mockbfl.New("right-key").Handler())
	defer mock.Close()
	a := newBFL(mock.URL, "wrong-key")
	_, err := a.CreateJob(context.Background(), &inference.CreateJobRequest{ID: "j", Task: "t2i", Prompt: "x"})
	var je *inference.JobError
	if !asJobError(err, &je) || je.Code != "invalid_input" || je.Retryable {
		t.Fatalf("want terminal invalid_input on bad key, got %v", err)
	}
	if _, err := a.GetManifest(context.Background()); err == nil {
		t.Fatal("manifest health must fail on bad key")
	}
}

// inlineInput drives the real download-and-encode path: bytes must survive
// the roundtrip exactly, and an oversize input must error loudly (never
// truncate — custody rule, input side).
func TestBFLInlineInput(t *testing.T) {
	data := pngBytes(t, 4, 4, color.RGBA{1, 2, 3, 255})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(data)
	}))
	defer srv.Close()
	a := newBFL("http://unused", "k")
	enc, err := a.inlineInput(context.Background(), srv.URL+"/in.png", "test")
	if err != nil {
		t.Fatal(err)
	}
	dec, err := base64.StdEncoding.DecodeString(enc)
	if err != nil || !bytes.Equal(dec, data) {
		t.Fatal("inlined bytes differ from source")
	}

	old := maxInlineInputBytes
	maxInlineInputBytes = 8
	defer func() { maxInlineInputBytes = old }()
	_, err = a.inlineInput(context.Background(), srv.URL+"/in.png", "test")
	var ve *inference.ValidationError
	if !asValidationError(err, &ve) {
		t.Fatalf("oversize input must be a loud validation error, got %v", err)
	}
}

// The 10-reference ceiling: the manifest caps upstream, so only a direct
// call exercises the adapter's own guard — pin it anyway (defense in depth
// against a manifest edit).
func TestBFLRefLimit(t *testing.T) {
	a, _, uploads := bflPair(t)
	ref := putFixture(t, uploads, "/r.png", pngBytes(t, 4, 4, color.RGBA{9, 9, 9, 255}))
	refs := make([]inference.Reference, 11)
	for i := range refs {
		refs[i] = inference.Reference{Kind: "image", Role: "object", URL: ref}
	}
	_, err := a.CreateJob(context.Background(), &inference.CreateJobRequest{
		ID: "j-refs", Task: "t2i", Prompt: "x", References: refs,
	})
	var ve *inference.ValidationError
	if !asValidationError(err, &ve) {
		t.Fatalf("11 refs must be a validation error, got %v", err)
	}
	// 10 refs is fine and the mock (strict field allowlist) accepts them.
	if _, err := a.CreateJob(context.Background(), &inference.CreateJobRequest{
		ID: "j-refs-10", Task: "t2i", Prompt: "x", References: refs[:10],
	}); err != nil {
		t.Fatalf("10 refs must submit: %v", err)
	}
}

// Poll-time Error defaults to transient (submit-time 422 carries the real
// validation story); recognizable moderation details classify.
func TestBFLPollErrorClassification(t *testing.T) {
	details := `"input was moderated"`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/flux-2-pro"):
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "t", "polling_url": "http://" + r.Host + "/v1/poll/t"})
		default:
			_, _ = w.Write([]byte(`{"id":"t","status":"Error","details":` + details + `}`))
		}
	}))
	defer srv.Close()
	a := newBFL(srv.URL, "k")
	if _, err := a.CreateJob(context.Background(), &inference.CreateJobRequest{ID: "j", Task: "t2i", Prompt: "x"}); err != nil {
		t.Fatal(err)
	}
	st, err := a.GetJob(context.Background(), "j")
	if err != nil || st.State != "failed" || st.Error == nil || st.Error.Code != "safety_blocked" {
		t.Fatalf("moderation details must classify safety_blocked failure, got %+v / %v", st, err)
	}
	details = `"gpu fell over"`
	a2 := newBFL(srv.URL, "k")
	_, _ = a2.CreateJob(context.Background(), &inference.CreateJobRequest{ID: "j2", Task: "t2i", Prompt: "x"})
	st, err = a2.GetJob(context.Background(), "j2")
	if err != nil || st.State != "failed" || st.Error == nil || st.Error.Code != "transient" || !st.Error.Retryable {
		t.Fatalf("unknown Error details must default transient failure, got %+v / %v", st, err)
	}
}

// Re-attach (PR 41): a fresh adapter instance (post-restart) attaches via
// the persisted handle and polls the SAME remote task to completion,
// bridging into the NEW attempt's upload target — no re-submission.
func TestBFLAttachJob(t *testing.T) {
	a, mock, uploads := bflPair(t)
	st, err := a.CreateJob(context.Background(), &inference.CreateJobRequest{
		ID: "job-a1", Task: "t2i", Prompt: "attach me", Seed: 7,
		Upload: &inference.Upload{Artifacts: []inference.UploadTarget{{PutURL: uploads.URL + "/a1.png", ContentType: "image/png"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Handle) == 0 {
		t.Fatal("CreateJob must return a handle")
	}

	// "Restart": a brand-new adapter instance with an empty job map.
	fresh := newBFL(mock.URL, "k")
	if err := fresh.AttachJob("job-a2", st.Handle, &inference.Upload{
		Artifacts: []inference.UploadTarget{{PutURL: uploads.URL + "/a2.png", ContentType: "image/png"}},
	}); err != nil {
		t.Fatal(err)
	}
	final, err := pollUntilTerminal(t, fresh, "job-a2")
	if err != nil {
		t.Fatal(err)
	}
	if !final.Artifacts[0].Uploaded {
		t.Fatal("attached job must bridge the artifact")
	}
	// The bridge landed in the NEW attempt's key.
	if res, _ := http.Get(uploads.URL + "/a2.png"); res.StatusCode != http.StatusOK {
		t.Fatal("artifact must land at the new attempt's target")
	}
	if err := fresh.AttachJob("j", json.RawMessage(`{"nope":1}`), nil); err == nil {
		t.Fatal("unusable handle must error (orchestrator falls back to re-submit)")
	}
}

// F1 regression: attaching to a DEAD remote task must surface a
// status-shaped failure (so the orchestrator clears the handle and the
// next attempt submits fresh) — never a transport-style error that would
// keep the corpse handle alive.
func TestBFLAttachToDeadTask(t *testing.T) {
	mock := httptest.NewServer(mockbfl.New("k").Handler())
	defer mock.Close()
	a := newBFL(mock.URL, "k")
	handle, _ := json.Marshal(map[string]string{"polling_url": mock.URL + "/v1/poll/never-existed"})
	if err := a.AttachJob("j-dead", handle, nil); err != nil {
		t.Fatal(err)
	}
	st, err := a.GetJob(context.Background(), "j-dead")
	if err != nil || st.State != "failed" || st.Error == nil || st.Error.Code != "transient" || !st.Error.Retryable {
		t.Fatalf("dead task must be a retryable status failure, got %+v / %v", st, err)
	}
}
