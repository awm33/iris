// orchestrator consumes the generation_jobs queue: resolves references to
// signed URLs, validates against capability manifests, dispatches via the
// inference API (spec/inference-api.md), polls with lease heartbeats, and
// lands artifacts as assets with full lineage.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/awm33/iris/backend/internal/blob"
	"github.com/awm33/iris/backend/internal/orchestrator"
	"github.com/awm33/iris/backend/internal/registry"
	"github.com/awm33/iris/backend/internal/store"
)

func main() {
	dsn := getenv("IRIS_PG_DSN", "postgres://iris:iris@localhost:15432/iris?sslmode=disable")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.New(ctx, dsn)
	if err != nil {
		slog.Error("store init", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	bl, err := blob.New(blob.Config{
		Endpoint:  getenv("IRIS_S3_ENDPOINT", "localhost:9100"),
		AccessKey: getenv("IRIS_S3_ACCESS_KEY", "iris"),
		SecretKey: getenv("IRIS_S3_SECRET_KEY", "iris-dev-secret"),
		Bucket:    getenv("IRIS_S3_BUCKET", "iris-media"),
		// Dockerized model endpoints reach host MinIO via host.docker.internal.
		ExternalEndpoint: getenv("IRIS_S3_EXTERNAL_ENDPOINT", "host.docker.internal:9100"),
	})
	if err != nil {
		slog.Error("blob init", "err", err)
		os.Exit(1)
	}

	reg := registry.New(st.Pool())
	if err := reg.Refresh(ctx); err != nil {
		slog.Warn("initial manifest refresh failed", "err", err)
	}
	go reg.RefreshLoop(ctx, time.Minute)

	host, _ := os.Hostname()
	o := &orchestrator.Orchestrator{
		Pool:     st.Pool(),
		Store:    st,
		Blob:     bl,
		Registry: reg,
		DSN:      dsn,
		Name:     host + "-orch-" + time.Now().Format("150405"),
		// Dev/test helper: identical resolved requests reuse landed
		// artifacts instead of re-billing the endpoint. Default OFF.
		CacheEnabled: os.Getenv("IRIS_GEN_CACHE") == "1" || os.Getenv("IRIS_GEN_CACHE") == "true",
	}
	if err := o.Run(ctx); err != nil && ctx.Err() == nil {
		slog.Error("orchestrator exited", "err", err)
		os.Exit(1)
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
