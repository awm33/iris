// media-worker runs ingest and prep jobs off the pg media_jobs queue.
// M1: probe (ffprobe metadata + video poster). M5 adds proxies, filmstrips,
// waveforms. ffmpeg/ffprobe come from PATH (containerized in deployment).
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/awm33/iris/backend/internal/blob"
	"github.com/awm33/iris/backend/internal/mediaworker"
)

func main() {
	dsn := getenv("IRIS_PG_DSN", "postgres://iris:iris@localhost:15432/iris?sslmode=disable")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		slog.Error("pg connect", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	bl, err := blob.New(blob.Config{
		Endpoint:  getenv("IRIS_S3_ENDPOINT", "localhost:9100"),
		AccessKey: getenv("IRIS_S3_ACCESS_KEY", "iris"),
		SecretKey: getenv("IRIS_S3_SECRET_KEY", "iris-dev-secret"),
		Bucket:    getenv("IRIS_S3_BUCKET", "iris-media"),
	})
	if err != nil {
		slog.Error("blob init", "err", err)
		os.Exit(1)
	}

	host, _ := os.Hostname()
	w := &mediaworker.Worker{
		Pool: pool,
		Blob: bl,
		DSN:  dsn,
		Name: host + "-" + time.Now().Format("150405"),
	}
	if err := w.Run(ctx); err != nil && ctx.Err() == nil {
		slog.Error("worker exited", "err", err)
		os.Exit(1)
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
