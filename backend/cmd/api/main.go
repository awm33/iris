// api is the core Connect-RPC service (iris.v1.*): workspaces/projects,
// assets/uploads, and (M2) generation jobs + the WebSocket event bridge.
//
// Auth v0: single-user dev mode — a seeded workspace (ws_dev) is attached to
// every request. Real sessions + OSS IdP land per the plan's backlog.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"connectrpc.com/connect"

	"github.com/awm33/iris/backend/gen/iris/v1/irisv1connect"
	"github.com/awm33/iris/backend/internal/api"
	"github.com/awm33/iris/backend/internal/blob"
	"github.com/awm33/iris/backend/internal/events"
	"github.com/awm33/iris/backend/internal/queue"
	"github.com/awm33/iris/backend/internal/registry"
	"github.com/awm33/iris/backend/internal/sources/pexels"
	"github.com/awm33/iris/backend/internal/store"
)

func main() {
	addr := getenv("IRIS_API_ADDR", ":8280") // 8080 commonly taken by other local stacks
	dsn := getenv("IRIS_PG_DSN", "postgres://iris:iris@localhost:15432/iris?sslmode=disable")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	st, err := store.New(ctx, dsn)
	if err != nil {
		slog.Error("store init", "err", err)
		os.Exit(1)
	}
	defer st.Close()
	if err := st.EnsureDevWorkspace(ctx, getenv("IRIS_DEV_EMAIL", "dev@local")); err != nil {
		slog.Error("dev workspace seed", "err", err)
		os.Exit(1)
	}

	bl, err := blob.New(blob.Config{
		Endpoint:       getenv("IRIS_S3_ENDPOINT", "localhost:9100"),
		AccessKey:      getenv("IRIS_S3_ACCESS_KEY", "iris"),
		SecretKey:      getenv("IRIS_S3_SECRET_KEY", "iris-dev-secret"),
		Bucket:         getenv("IRIS_S3_BUCKET", "iris-media"),
		PublicEndpoint: os.Getenv("IRIS_S3_PUBLIC_ENDPOINT"), // set when browser reaches MinIO differently
		// Dockerized tool services (SAM) fetch via host.docker.internal —
		// same contract as the orchestrator's model-endpoint presigns.
		ExternalEndpoint: getenv("IRIS_S3_EXTERNAL_ENDPOINT", "host.docker.internal:9100"),
	})
	if err != nil {
		slog.Error("blob init", "err", err)
		os.Exit(1)
	}

	reg := registry.New(st.Pool())
	if err := reg.SeedDevEndpoints(ctx, store.DevWorkspaceID, map[string]registry.DevSeed{
		"Mock Video (dev)":  {BaseURL: getenv("IRIS_MOCK_VIDEO_URL", "http://localhost:8900")},
		"Mock Image (dev)":  {BaseURL: getenv("IRIS_MOCK_IMAGE_URL", "http://localhost:8901")},
		"LaMa Remove (dev)": {BaseURL: getenv("IRIS_LAMA_URL", "http://localhost:8902")},
		// First commercial adapter, against the recorded-shape mock. The
		// auth_ref is a VAULT reference — the key itself lives in the
		// orchestrator's env, resolved only at dispatch.
		"Seedance (dev mock)": {
			BaseURL: getenv("IRIS_SEEDANCE_URL", "http://127.0.0.1:8905"), // 127.0.0.1: the mock binds IPv4; localhost resolves ::1 first
			Kind:    "seedance",
			AuthRef: "env:MOCK_SEEDANCE_KEY",
		},
	}); err != nil {
		slog.Error("endpoint seed", "err", err)
		os.Exit(1)
	}
	if err := reg.Refresh(ctx); err != nil {
		slog.Warn("initial manifest refresh failed (endpoints may be down)", "err", err)
	}
	go reg.RefreshLoop(context.Background(), time.Minute)

	bridge := &events.Bridge{DSN: dsn, Channels: []string{queue.GenerationChannel, queue.MediaChannel}}
	go bridge.Run(context.Background())

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	mux.Handle("GET /events", bridge)
	// Body-size ceiling for every RPC: request payloads are otherwise fully
	// buffered before the per-field caps run. 16MB clears the largest
	// legitimate call (AppendOps: 200 ops × 64KB) with headroom.
	opts := connect.WithReadMaxBytes(16 << 20)
	mux.Handle(irisv1connect.NewWorkspaceServiceHandler(&api.WorkspaceServer{Store: st}, opts))
	pex := pexels.New(os.Getenv("IRIS_PEXELS_API_KEY"))
	if pex == nil {
		slog.Info("pexels source not configured (IRIS_PEXELS_API_KEY unset)")
	}
	mux.Handle(irisv1connect.NewAssetServiceHandler(&api.AssetServer{Store: st, Blob: bl, Pexels: pex}, opts))
	mux.Handle(irisv1connect.NewGenerationServiceHandler(&api.GenerationServer{Store: st, Registry: reg}, opts))
	mux.Handle(irisv1connect.NewStoryServiceHandler(&api.StoryServer{Store: st}, opts))
	mux.Handle(irisv1connect.NewTimelineServiceHandler(&api.TimelineServer{Store: st}, opts))
	mux.Handle(irisv1connect.NewCanvasServiceHandler(&api.CanvasServer{
		Store: st, Blob: bl,
		SamURL: getenv("IRIS_SAM_URL", "http://localhost:8903"),
		HTTP:   &http.Client{Timeout: 60 * time.Second},
	}, opts))

	slog.Info("iris-api listening", "addr", addr)
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second, // slowloris guard; no WriteTimeout (SSE streams)
	}
	if err := srv.ListenAndServe(); err != nil {
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
