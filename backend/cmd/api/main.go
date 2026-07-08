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

	"github.com/awm33/iris/backend/gen/iris/v1/irisv1connect"
	"github.com/awm33/iris/backend/internal/api"
	"github.com/awm33/iris/backend/internal/blob"
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
	})
	if err != nil {
		slog.Error("blob init", "err", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	mux.Handle(irisv1connect.NewWorkspaceServiceHandler(&api.WorkspaceServer{Store: st}))
	mux.Handle(irisv1connect.NewAssetServiceHandler(&api.AssetServer{Store: st, Blob: bl}))
	// TODO(M2): GenerationService + /ws event bridge (pg LISTEN 'jobs' → WS fan-out)

	slog.Info("iris-api listening", "addr", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
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
