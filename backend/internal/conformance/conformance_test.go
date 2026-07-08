package conformance

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/awm33/iris/backend/internal/mockmodel"
)

// TestMockModelConformance is the gate that keeps the reference
// implementation and the spec in agreement: the mock must pass every check,
// including failure injection.
func TestMockModelConformance(t *testing.T) {
	srv := httptest.NewServer(mockmodel.Handler(mockmodel.Options{
		Modality: "video",
		RunFor:   300 * time.Millisecond, // keep CI fast
	}))
	defer srv.Close()

	results := Run(context.Background(), Config{
		BaseURL:          srv.URL,
		Token:            "test",
		FailureInjection: true,
		PollInterval:     50 * time.Millisecond,
		Timeout:          30 * time.Second,
	})
	for _, r := range results {
		if r.Skipped {
			t.Errorf("%s: unexpectedly skipped", r.Name)
			continue
		}
		if r.Err != nil {
			t.Errorf("%s: %v", r.Name, r.Err)
		} else {
			t.Logf("%s: ok %s", r.Name, r.Detail)
		}
	}
}

func TestImageModalityConformance(t *testing.T) {
	srv := httptest.NewServer(mockmodel.Handler(mockmodel.Options{
		Modality: "image",
		RunFor:   100 * time.Millisecond,
	}))
	defer srv.Close()

	results := Run(context.Background(), Config{
		BaseURL: srv.URL, Token: "test",
		PollInterval: 50 * time.Millisecond, Timeout: 30 * time.Second,
	})
	for _, r := range results {
		if !r.Skipped && r.Err != nil {
			t.Errorf("%s: %v", r.Name, r.Err)
		}
	}
}

// TestSchemaCopyInSync guards the embedded schema copy against drift from the
// canonical spec/manifest.schema.json.
func TestSchemaCopyInSync(t *testing.T) {
	canonical, err := os.ReadFile("../../../spec/manifest.schema.json")
	if err != nil {
		t.Fatalf("read canonical schema: %v", err)
	}
	if !bytes.Equal(bytes.TrimSpace(canonical), bytes.TrimSpace(manifestSchema)) {
		t.Fatal("backend/internal/conformance/manifest.schema.json has drifted from spec/manifest.schema.json — copy it over")
	}
}
