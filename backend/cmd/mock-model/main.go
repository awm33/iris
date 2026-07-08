// mock-model serves the reference implementation of spec/inference-api.md.
// See backend/internal/mockmodel for behavior and failure-injection docs.
package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/awm33/iris/backend/internal/mockmodel"
)

func main() {
	addr := getenv("MOCK_MODEL_ADDR", ":8900")
	modality := getenv("MOCK_MODEL_MODALITY", "video") // video | image

	slog.Info("mock-model listening", "addr", addr, "modality", modality)
	if err := http.ListenAndServe(addr, mockmodel.Handler(mockmodel.Options{Modality: modality})); err != nil {
		slog.Error("server exited", "err", err)
		os.Exit(1)
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
