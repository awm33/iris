package adapters

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/awm33/iris/backend/internal/inference"
	"github.com/awm33/iris/backend/internal/mockseedance"
)

// The translator's contract, exercised against the recorded-shape mock —
// exactly the lifecycle/taxonomy/custody mappings the orchestrator relies
// on. (The review of this PR is the motivating case: three of its findings
// were one table test away.)

func testStack(t *testing.T) (*seedance, *httptest.Server, *httptest.Server, *[]byte) {
	t.Helper()
	mock := httptest.NewServer(mockseedance.New("k").Handler())
	t.Cleanup(mock.Close)
	var uploaded []byte
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		uploaded = body
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(sink.Close)
	return newSeedance(mock.URL, "k"), mock, sink, &uploaded
}

func waitTerminal(t *testing.T, sd *seedance, id string) *inference.JobStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st, err := sd.GetJob(context.Background(), id)
		if err != nil {
			return &inference.JobStatus{State: "error", Error: &inference.JobError{Message: err.Error()}}
		}
		if st.Terminal() {
			return st
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("job never terminal")
	return nil
}

func TestSeedanceLifecycleAndCustody(t *testing.T) {
	sd, _, sink, uploaded := testStack(t)
	st, err := sd.CreateJob(context.Background(), &inference.CreateJobRequest{
		ID: "att-1", Task: "t2v", Prompt: "hello kitchen", Seed: 7,
		Output: &inference.Output{Width: 864, Height: 480, DurationS: 5},
		Upload: &inference.Upload{Artifacts: []inference.UploadTarget{{PutURL: sink.URL + "/up", ContentType: "video/mp4"}}},
	})
	if err != nil || st.State != "queued" {
		t.Fatalf("create: %v %+v", err, st)
	}
	final := waitTerminal(t, sd, "att-1")
	if final.State != "complete" {
		t.Fatalf("want complete, got %+v", final)
	}
	if len(final.Artifacts) != 1 || !final.Artifacts[0].Uploaded || final.Artifacts[0].SHA256 == "" {
		t.Fatalf("artifact custody broken: %+v", final.Artifacts)
	}
	if len(*uploaded) == 0 {
		t.Fatal("nothing was PUT to the presigned target")
	}
	if final.Metrics == nil || final.Metrics.GPUSeconds != 5 {
		t.Fatalf("billable seconds not reported: %+v", final.Metrics)
	}
}

func TestSeedancePolicyMapsToSafetyBlocked(t *testing.T) {
	sd, _, sink, _ := testStack(t)
	_, err := sd.CreateJob(context.Background(), &inference.CreateJobRequest{
		ID: "att-2", Task: "t2v", Prompt: "unsafe thing",
		Upload: &inference.Upload{Artifacts: []inference.UploadTarget{{PutURL: sink.URL + "/up"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	final := waitTerminal(t, sd, "att-2")
	if final.State != "failed" || final.Error == nil || final.Error.Code != "safety_blocked" {
		t.Fatalf("want safety_blocked failure, got %+v err=%+v", final, final.Error)
	}
}

func TestSeedanceBadKeyIsUnhealthyAndNonRetryable(t *testing.T) {
	mock := httptest.NewServer(mockseedance.New("right-key").Handler())
	defer mock.Close()
	sd := newSeedance(mock.URL, "wrong-key")
	if _, err := sd.GetManifest(context.Background()); err == nil {
		t.Fatal("401 ping must make the endpoint unhealthy, not healthy")
	}
	_, err := sd.CreateJob(context.Background(), &inference.CreateJobRequest{ID: "att-3", Prompt: "x"})
	var jerr *inference.JobError
	if !asJobError(err, &jerr) || jerr.Retryable {
		t.Fatalf("auth failure must be non-retryable taxonomy, got %v", err)
	}
}

func asJobError(err error, out **inference.JobError) bool {
	e, ok := err.(*inference.JobError)
	if ok {
		*out = e
	}
	return ok
}

func TestSeedanceOversizeResultErrorsInsteadOfTruncating(t *testing.T) {
	old := maxArtifactBytes
	maxArtifactBytes = 64
	defer func() { maxArtifactBytes = old }()
	sd, _, sink, _ := testStack(t)
	_, err := sd.CreateJob(context.Background(), &inference.CreateJobRequest{
		ID: "att-4", Task: "t2v", Prompt: "big",
		Upload: &inference.Upload{Artifacts: []inference.UploadTarget{{PutURL: sink.URL + "/up"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, gerr := sd.GetJob(context.Background(), "att-4")
		if gerr != nil {
			if !strings.Contains(gerr.Error(), "cap") && !strings.Contains(gerr.Error(), "transfer") {
				t.Fatalf("want a capped-transfer error, got %v", gerr)
			}
			return // errored instead of landing truncated bytes ✓
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("oversize result silently succeeded")
}

func TestSeedanceAudioRefBecomesLipSyncContent(t *testing.T) {
	// A recording endpoint (not the mock): asserts the exact recorded shape
	// the adapter emits for an audio reference.
	var created []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			created, _ = io.ReadAll(r.Body)
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "cgt-1"})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	sd := newSeedance(srv.URL, "k")
	_, err := sd.CreateJob(context.Background(), &inference.CreateJobRequest{
		ID: "att-a", Task: "i2v", Prompt: "she speaks",
		References: []inference.Reference{{Kind: "audio", Role: "speech_lipsync", URL: "http://x/voice.mp3"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Content []struct {
			Type     string `json:"type"`
			Role     string `json:"role"`
			AudioURL *struct {
				URL string `json:"url"`
			} `json:"audio_url"`
		} `json:"content"`
	}
	if err := json.Unmarshal(created, &body); err != nil {
		t.Fatal(err)
	}
	for _, c := range body.Content {
		if c.Type == "audio_url" && c.Role == "lip_sync" && c.AudioURL != nil && c.AudioURL.URL == "http://x/voice.mp3" {
			return
		}
	}
	t.Fatalf("audio reference did not become a lip_sync audio_url content item: %s", created)
}

// The Ark text-command channel belongs to the adapter: user "--" tokens
// are stripped (orphaned values stay as harmless prompt words).
func TestStripArkFlags(t *testing.T) {
	cases := map[string]string{
		"a quiet diner":                        "a quiet diner",
		"a diner --duration 99":                "a diner 99",
		"--resolution 4k neon rain --seed 1 x": "4k neon rain 1 x",
		"--":                                   "",
	}
	for in, want := range cases {
		if got := stripArkFlags(in); got != want {
			t.Errorf("stripArkFlags(%q) = %q, want %q", in, got, want)
		}
	}
}

// Re-attach (PR 41): a fresh seedance instance attaches via the persisted
// handle, polls the SAME Ark task to completion, bridges into the NEW
// attempt's upload, and cancel-after-attach reaches the remote task (the
// map entry the attach seeded).
func TestSeedanceAttachJob(t *testing.T) {
	sd, mock, sink, uploaded := testStack(t)
	st, err := sd.CreateJob(context.Background(), &inference.CreateJobRequest{
		ID: "att-a1", Task: "t2v", Prompt: "reattach me", Seed: 3,
		Output: &inference.Output{Width: 864, Height: 480, DurationS: 5},
		Upload: &inference.Upload{Artifacts: []inference.UploadTarget{{PutURL: sink.URL + "/a1", ContentType: "video/mp4"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Handle) == 0 {
		t.Fatal("CreateJob must return a handle")
	}

	fresh := newSeedance(mock.URL, "k")
	if err := fresh.AttachJob("att-a2", st.Handle, &inference.Upload{
		Artifacts: []inference.UploadTarget{{PutURL: sink.URL + "/a2", ContentType: "video/mp4"}},
	}); err != nil {
		t.Fatal(err)
	}
	final := waitTerminal(t, fresh, "att-a2")
	if final.State != "complete" || len(*uploaded) == 0 {
		t.Fatalf("attached job must complete and bridge: %+v", final)
	}

	// Cancel path after attach: the seeded map entry must reach the remote.
	another := newSeedance(mock.URL, "k")
	if err := another.AttachJob("att-a3", st.Handle, nil); err != nil {
		t.Fatal(err)
	}
	if err := another.CancelJob(context.Background(), "att-a3"); err != nil {
		t.Fatalf("cancel after attach must reach the remote task: %v", err)
	}

	if err := fresh.AttachJob("bad", json.RawMessage(`{"x":1}`), nil); err == nil {
		t.Fatal("unusable handle must error")
	}
}
