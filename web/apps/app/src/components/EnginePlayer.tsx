import { useEffect, useRef, useState } from "react";
import { ClipDecoder, FrameQueue } from "@iris/media-engine";

// WebCodecs testbed (compositor arc, PR 22): decode the proxy with the
// media engine and paint to canvas. Proves demux → decode → present on
// real prep output before the timeline compositor builds on it. Play-once
// with replay; the <video> path stays the product player until PR 23.
export function EnginePlayer(props: { src: string }) {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const [status, setStatus] = useState("opening…");
  const [replayNonce, setReplayNonce] = useState(0);

  useEffect(() => {
    const abort = new AbortController();
    let raf = 0;
    // Small on purpose: every queued frame pins a hardware decoder buffer;
    // Chrome's pool is ~8-10 and the decoder wedges when they're all out.
    const queue = new FrameQueue<VideoFrame>(3);
    (async () => {
      try {
        const t0open = performance.now();
        const dec = await ClipDecoder.open(props.src, abort.signal);
        const canvas = canvasRef.current;
        if (!canvas || abort.signal.aborted) return;
        canvas.width = dec.info.width;
        canvas.height = dec.info.height;
        const ctx = canvas.getContext("2d")!;
        setStatus(`decoding ${dec.info.codec} ${dec.info.width}×${dec.info.height} (opened in ${Math.round(performance.now() - t0open)}ms)`);

        let pumping = true;
        let pumpFailed = false;
        void (async () => {
          for await (const frame of dec.frames(0, abort.signal)) {
            // Backpressure: the painter drains by wall clock.
            while (queue.full && !abort.signal.aborted) {
              await new Promise((r) => setTimeout(r, 10));
            }
            if (abort.signal.aborted) {
              frame.close();
              return;
            }
            queue.push(frame);
          }
        })()
          .catch((e) => {
            // The testbed exists to prove decode works — a decode failure
            // must never read as "done".
            pumpFailed = true;
            if (!abort.signal.aborted) setStatus(`decode failed: ${String(e)}`);
          })
          .finally(() => {
            pumping = false;
          });

        let painted = 0;
        const t0 = performance.now();
        const paint = () => {
          if (abort.signal.aborted) return;
          const tUs = (performance.now() - t0) * 1000;
          const frame = queue.takeUpTo(tUs);
          if (frame) {
            ctx.drawImage(frame, 0, 0);
            frame.close();
            painted++;
          }
          if (!pumping && queue.size === 0) {
            if (!pumpFailed) {
              const secs = (performance.now() - t0) / 1000;
              setStatus(`done — ${painted} frames painted in ${secs.toFixed(1)}s (${(painted / secs).toFixed(1)} fps)`);
            }
            return;
          }
          raf = requestAnimationFrame(paint);
        };
        raf = requestAnimationFrame(paint);
      } catch (e) {
        if (!abort.signal.aborted) setStatus(`engine failed: ${String(e)}`);
      }
    })();
    return () => {
      abort.abort();
      cancelAnimationFrame(raf);
      queue.clear();
    };
  }, [props.src, replayNonce]);

  return (
    <div className="engine-player">
      <canvas ref={canvasRef} />
      <div className="toolbar" style={{ marginBottom: 0 }}>
        <button className="btn secondary" onClick={() => setReplayNonce((n) => n + 1)}>⟳ Replay</button>
        <span className="meta">{status}</span>
      </div>
    </div>
  );
}
