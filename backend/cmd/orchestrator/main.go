// orchestrator claims generation jobs from the pg queue (SKIP LOCKED +
// LISTEN/NOTIFY), resolves references/conditioning to signed URLs, validates
// against the endpoint's capability manifest, dispatches via the inference
// API (spec/inference-api.md), and lands artifacts + lineage atomically.
// Implementation is M2; see backend/internal/queue for the claim design.
package main

import "log/slog"

func main() {
	slog.Info("orchestrator stub — implementation lands in M2 (docs/design/04-implementation-plan.md)")
	select {}
}
