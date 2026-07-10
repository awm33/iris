package adapters

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/awm33/iris/backend/internal/inference"
	"github.com/awm33/iris/backend/internal/mockelevenlabs"
)

func elevenStack(t *testing.T, key string) (*elevenlabs, *httptest.Server, *[]byte) {
	t.Helper()
	mock := httptest.NewServer(mockelevenlabs.New("k").Handler())
	t.Cleanup(mock.Close)
	var uploaded []byte
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		uploaded = body
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(sink.Close)
	return newElevenLabs(mock.URL, key), sink, &uploaded
}

func TestElevenLabsSynchronousLifecycle(t *testing.T) {
	el, sink, uploaded := elevenStack(t, "k")
	if _, err := el.GetManifest(context.Background()); err != nil {
		t.Fatalf("healthy key must pass: %v", err)
	}
	st, err := el.CreateJob(context.Background(), &inference.CreateJobRequest{
		ID: "att-1", Task: "tts", Prompt: "MARA: The sauce is done.",
		Upload: &inference.Upload{Artifacts: []inference.UploadTarget{{PutURL: sink.URL + "/up", ContentType: "audio/mpeg"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.State != "complete" || len(st.Artifacts) != 1 || !st.Artifacts[0].Uploaded || st.Artifacts[0].SHA256 == "" {
		t.Fatalf("synchronous completion broken: %+v", st)
	}
	if st.Artifacts[0].ContentType != "audio/mpeg" {
		t.Fatalf("want audio/mpeg, got %s", st.Artifacts[0].ContentType)
	}
	if len(*uploaded) == 0 {
		t.Fatal("nothing was PUT to the presigned target")
	}
	if st.Metrics == nil || st.Metrics.GPUSeconds != 1 {
		t.Fatalf("usd_per_job metering missing: %+v", st.Metrics)
	}
	// GetJob replays the terminal status without a second remote call.
	again, err := el.GetJob(context.Background(), "att-1")
	if err != nil || again.State != "complete" {
		t.Fatalf("terminal replay: %v %+v", err, again)
	}
}

func TestElevenLabsBadKeyUnhealthyAndNonRetryable(t *testing.T) {
	el, sink, _ := elevenStack(t, "wrong")
	if _, err := el.GetManifest(context.Background()); err == nil {
		t.Fatal("401 voices must read unhealthy")
	}
	_, err := el.CreateJob(context.Background(), &inference.CreateJobRequest{
		ID: "att-2", Task: "tts", Prompt: "x",
		Upload: &inference.Upload{Artifacts: []inference.UploadTarget{{PutURL: sink.URL + "/up"}}},
	})
	jerr, ok := err.(*inference.JobError)
	if !ok || jerr.Retryable {
		t.Fatalf("auth failure must be non-retryable taxonomy, got %v", err)
	}
}

func TestElevenLabsEmptyTextIsInvalidInput(t *testing.T) {
	el, sink, _ := elevenStack(t, "k")
	_, err := el.CreateJob(context.Background(), &inference.CreateJobRequest{
		ID: "att-3", Task: "tts", Prompt: "",
		Upload: &inference.Upload{Artifacts: []inference.UploadTarget{{PutURL: sink.URL + "/up"}}},
	})
	if _, ok := err.(*inference.ValidationError); !ok {
		t.Fatalf("empty text must be a ValidationError (invalid_input park), got %v", err)
	}
}

func TestElevenLabsOversizeErrors(t *testing.T) {
	old := maxArtifactBytes
	maxArtifactBytes = 64
	defer func() { maxArtifactBytes = old }()
	el, sink, _ := elevenStack(t, "k")
	_, err := el.CreateJob(context.Background(), &inference.CreateJobRequest{
		ID: "att-4", Task: "tts", Prompt: "long speech",
		Upload: &inference.Upload{Artifacts: []inference.UploadTarget{{PutURL: sink.URL + "/up"}}},
	})
	if err == nil {
		t.Fatal("oversize audio must error, never truncate-and-land")
	}
}
