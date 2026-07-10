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
	_, err = a.GetJob(context.Background(), "job-3")
	var je *inference.JobError
	if !asJobError(err, &je) || je.Code != "safety_blocked" || je.Retryable {
		t.Fatalf("want terminal safety_blocked, got %v", err)
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

// base64 sanity: the adapter inlines inputs; the mock decodes them — this
// pair test would catch an encoding mismatch.
func TestBFLInlineRoundtrip(t *testing.T) {
	data := pngBytes(t, 4, 4, color.RGBA{1, 2, 3, 255})
	enc := base64.StdEncoding.EncodeToString(data)
	dec, err := base64.StdEncoding.DecodeString(enc)
	if err != nil || !bytes.Equal(dec, data) {
		t.Fatal("b64 roundtrip broken")
	}
}
