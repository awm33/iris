package adapters

import (
	"context"
	"encoding/json"
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

func TestElevenLabsOverloadMapsToOverloaded(t *testing.T) {
	mock := httptest.NewServer(mockelevenlabs.New("k").Handler())
	defer mock.Close()
	el := newElevenLabs(mock.URL, "k")
	// The mock's overload simulation header exists for exactly this test.
	el.http.Transport = headerInjector{next: http.DefaultTransport}
	_, err := el.CreateJob(context.Background(), &inference.CreateJobRequest{ID: "att-5", Task: "tts", Prompt: "x"})
	jerr, ok := err.(*inference.JobError)
	if !ok || jerr.Code != "overloaded" || !jerr.Retryable {
		t.Fatalf("429 must map to retryable overloaded, got %v", err)
	}
}

type headerInjector struct{ next http.RoundTripper }

func (h headerInjector) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("X-Mock-Overload", "1")
	return h.next.RoundTrip(r)
}

func TestElevenLabsPutFailureIsTaxonomyTransient(t *testing.T) {
	el, _, _ := elevenStack(t, "k")
	badSink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer badSink.Close()
	_, err := el.CreateJob(context.Background(), &inference.CreateJobRequest{
		ID: "att-6", Task: "tts", Prompt: "x",
		Upload: &inference.Upload{Artifacts: []inference.UploadTarget{{PutURL: badSink.URL + "/up"}}},
	})
	jerr, ok := err.(*inference.JobError)
	if !ok || jerr.Code != "transient" || !jerr.Retryable {
		t.Fatalf("PUT failure after paid tts must be a taxonomy transient (never a url.Error chain), got %v", err)
	}
}

func TestElevenLabsVoiceParamRoutesAndUnknownParks(t *testing.T) {
	el, sink, _ := elevenStack(t, "k")
	// mara is a mock-known voice: the param must reach the URL path.
	st, err := el.CreateJob(context.Background(), &inference.CreateJobRequest{
		ID: "att-7", Task: "tts", Prompt: "hi", Params: []byte(`{"voice_id":"mara"}`),
		Upload: &inference.Upload{Artifacts: []inference.UploadTarget{{PutURL: sink.URL + "/up"}}},
	})
	if err != nil || st.State != "complete" {
		t.Fatalf("known voice must generate: %v %+v", err, st)
	}
	// Unknown voices 404 remotely → non-retryable invalid_input park.
	_, err = el.CreateJob(context.Background(), &inference.CreateJobRequest{
		ID: "att-8", Task: "tts", Prompt: "hi", Params: []byte(`{"voice_id":"nobody"}`),
		Upload: &inference.Upload{Artifacts: []inference.UploadTarget{{PutURL: sink.URL + "/up"}}},
	})
	jerr, ok := err.(*inference.JobError)
	if !ok || jerr.Retryable || jerr.Code != "invalid_input" {
		t.Fatalf("unknown voice must park invalid_input, got %v", err)
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

func TestElevenLabsManifestVoicesSubsetOfMock(t *testing.T) {
	var m struct {
		ParamsSchema struct {
			Properties struct {
				VoiceID struct {
					Enum []string `json:"enum"`
				} `json:"voice_id"`
			} `json:"properties"`
		} `json:"params_schema"`
	}
	if err := json.Unmarshal(elevenLabsManifest, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.ParamsSchema.Properties.VoiceID.Enum) == 0 {
		t.Fatal("manifest declares no voices")
	}
	known := map[string]bool{}
	for _, v := range mockelevenlabs.Voices {
		known[v] = true
	}
	for _, v := range m.ParamsSchema.Properties.VoiceID.Enum {
		if !known[v] {
			t.Fatalf("manifest voice %q is unknown to the mock — the two lists drifted", v)
		}
	}
}
