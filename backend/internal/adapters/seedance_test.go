package adapters

import (
	"context"
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
