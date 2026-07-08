// media-worker runs ingest and prep jobs off the pg queue: probe, proxy
// transcode, filmstrips, waveforms, first/last-frame extraction, embeddings
// (ONNX in-process), and depth-sequence rendering. ffmpeg is invoked inside
// this container only — no host ffmpeg dependencies. Implementation starts
// in M1 (ingest probe) and grows through M5 (proxies) per the plan.
package main

import "log/slog"

func main() {
	slog.Info("media-worker stub — ingest probe lands in M1 (docs/design/04-implementation-plan.md)")
	select {}
}
