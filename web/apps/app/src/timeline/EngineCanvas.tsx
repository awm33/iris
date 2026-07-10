import { useEffect, useRef } from "react";
import {
  ClipDecoder,
  FrameQueue,
  prebufferTarget,
  type Segment,
  segmentAt,
  sourceTime,
} from "@iris/media-engine";

// Timeline compositor (PR 23/24): the media engine paints resolved video
// segments to one canvas, chasing the page's wall-clock playhead. Gapless
// boundaries come from prebuffering the next segment's decoder inside
// PREBUFFER_S. Audio is the AudioMixer's job (scheduled separately on the
// AudioContext clock); together they are the timeline's default player.
//
// Ownership: this component OWNS decoders, generators and queued frames;
// TimelinePage owns the clock (time/playing props).

const PREBUFFER_S = 1.5;
const MAX_DECODERS = 4; // each holds a full proxy in RAM — LRU beyond this

interface PlaySession {
  sourceId: string;
  queue: FrameQueue<VideoFrame>;
  abort: AbortController;
  /** Source-local µs of the last painted frame (backward-jump detection). */
  lastPaintedUs: number;
  done: boolean;
}

export function EngineCanvas(props: {
  segments: Segment[];
  time: number;
  playing: boolean;
  /** Per-clip color grades (incl. any in-flight slider draft) — kept OFF
   * the segments array so a grade tick never respawns decode sessions. */
  grades?: Map<string, { exposure: number; contrast: number; temp: number }>;
  /** Resolve a segment source to a fetchable (signed) URL. */
  srcFor: (sourceId: string) => Promise<string>;
  onError?: (message: string) => void;
}) {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  // Raw (ungraded) copy of the last painted frame: VideoFrames must close
  // promptly, but a paused grade change still needs pixels to recomposite.
  const rawRef = useRef<HTMLCanvasElement | null>(null);
  const decodersRef = useRef(new Map<string, Promise<ClipDecoder>>());
  const sessionRef = useRef<PlaySession | null>(null);
  const scrubAbortRef = useRef<AbortController | null>(null);
  const scrubKeyRef = useRef<string | null>(null);
  const propsRef = useRef(props);
  useEffect(() => {
    propsRef.current = props;
  });

  const decoderFor = (sourceId: string): Promise<ClipDecoder> => {
    const cache = decodersRef.current;
    const hit = cache.get(sourceId);
    if (hit) {
      // LRU: re-insert on use.
      cache.delete(sourceId);
      cache.set(sourceId, hit);
      return hit;
    }
    const opened = propsRef.current.srcFor(sourceId).then((url) => ClipDecoder.open(url));
    opened.catch(() => {
      // Only delete OUR entry: an evicted-then-reopened source must not
      // have its healthy replacement removed by the stale rejection.
      if (cache.get(sourceId) === opened) cache.delete(sourceId);
    });
    cache.set(sourceId, opened);
    // Evict oldest first, SKIPPING the playing source — a break here would
    // stop evicting entirely and grow the cache unbounded once the playing
    // source ages to the front.
    for (const key of [...cache.keys()]) {
      if (cache.size <= MAX_DECODERS) break;
      if (key === sessionRef.current?.sourceId) continue;
      cache.delete(key);
    }
    return opened;
  };

  const endSession = () => {
    sessionRef.current?.abort.abort();
    sessionRef.current?.queue.clear();
    sessionRef.current = null;
  };

  const paintFrame = (frame: VideoFrame) => {
    const raw = (rawRef.current ??= document.createElement("canvas"));
    if (raw.width !== frame.displayWidth || raw.height !== frame.displayHeight) {
      raw.width = frame.displayWidth;
      raw.height = frame.displayHeight;
    }
    raw.getContext("2d")!.drawImage(frame, 0, 0);
    frame.close();
    composite();
  };

  // composite applies the active clip's grade raw → visible. The math is
  // the EXPORT's lutrgb pipeline exactly (plan PR 36): brightness(2^stops)
  // then contrast() — each clamped, per channel, sRGB — then an
  // attenuation-only per-channel multiply for temperature (gains ≤ 1 by
  // construction; a multiply blend cannot brighten).
  const composite = () => {
    const canvas = canvasRef.current;
    const raw = rawRef.current;
    if (!canvas || !raw || raw.width === 0) return;
    const { segments, time, grades } = propsRef.current;
    const seg = segmentAt(segments, time);
    const ctx = canvas.getContext("2d")!;
    if (!seg) {
      // Stale raw pixels must not recomposite into a gap (grade-change
      // repaint while the playhead sits on no clip).
      ctx.clearRect(0, 0, canvas.width, canvas.height);
      return;
    }
    if (canvas.width !== raw.width || canvas.height !== raw.height) {
      canvas.width = raw.width;
      canvas.height = raw.height;
    }
    const grade = seg.clipId ? grades?.get(seg.clipId) : undefined;
    if (grade && (grade.exposure !== 0 || grade.contrast !== 1)) {
      ctx.filter = `brightness(${2 ** grade.exposure}) contrast(${grade.contrast})`;
    }
    ctx.drawImage(raw, 0, 0);
    ctx.filter = "none";
    if (grade && grade.temp !== 0) {
      const t = grade.temp;
      const rG = t < 0 ? 1 + 0.3 * t : 1;
      const gG = t > 0 ? 1 - 0.1 * t : 1 + 0.1 * t;
      const bG = t > 0 ? 1 - 0.3 * t : 1;
      ctx.save();
      ctx.globalCompositeOperation = "multiply";
      ctx.fillStyle = `rgb(${Math.round(255 * rG)}, ${Math.round(255 * gG)}, ${Math.round(255 * bG)})`;
      ctx.fillRect(0, 0, canvas.width, canvas.height);
      ctx.restore();
    }
  };

  const startSession = (sourceId: string, fromS: number) => {
    endSession();
    const abort = new AbortController();
    const session: PlaySession = {
      sourceId,
      queue: new FrameQueue<VideoFrame>(3),
      abort,
      lastPaintedUs: Math.round(fromS * 1e6),
      done: false,
    };
    sessionRef.current = session;
    void (async () => {
      const dec = await decoderFor(sourceId);
      if (abort.signal.aborted) return;
      for await (const frame of dec.frames(fromS, abort.signal)) {
        while (session.queue.full && !abort.signal.aborted) {
          await new Promise((r) => setTimeout(r, 10));
        }
        if (abort.signal.aborted) {
          frame.close();
          return;
        }
        session.queue.push(frame);
      }
    })()
      .catch((e) => {
        if (!abort.signal.aborted) {
          propsRef.current.onError?.(`engine: ${String(e)}`);
          // A dead session must not pin the canvas for the rest of the
          // segment — clearing it lets the next tick restart (retry).
          if (sessionRef.current === session) {
            endSession();
          }
        }
      })
      .finally(() => {
        session.done = true;
      });
  };

  // Playback painting: chase the playhead each time tick.
  useEffect(() => {
    const seg = segmentAt(props.segments, props.time);
    if (props.playing) {
      scrubAbortRef.current?.abort();
      if (!seg) {
        // Gap or unresolved span: keep the session for a same-source
        // return? No — timelines are short; a gap ends the session and
        // blanks the canvas (placeholder/still overlays render above us).
        endSession();
        const c = canvasRef.current;
        if (c) c.getContext("2d")!.clearRect(0, 0, c.width, c.height);
        // Still spans are first-class sequence shapes now: without a
        // prebuffer HERE, every still→video cut stalls the picture for a
        // sign+fetch+parse while the statically-scheduled audio runs on
        // time. prebufferTarget handles current===undefined.
        const pre = prebufferTarget(props.segments, props.time, PREBUFFER_S);
        if (pre) void decoderFor(pre.sourceId).catch(() => {});
        return;
      }
      const targetUs = Math.round(sourceTime(seg, props.time) * 1e6);
      const s = sessionRef.current;
      // (Re)start on: source switch; a backward jump the decode-forward
      // generator can't serve; or a same-source FORWARD discontinuity —
      // blade+delete cuts jump the in-point within one source, and chasing
      // it by decoding through the removed span would smear exactly the
      // footage the editor cut. Forward restarts are gated on !done so the
      // overhang freeze (source shorter than clip) doesn't churn restarts.
      if (
        !s ||
        s.sourceId !== seg.sourceId ||
        targetUs < s.lastPaintedUs - 250_000 ||
        (!s.done && targetUs > s.lastPaintedUs + 1_000_000)
      ) {
        startSession(seg.sourceId, sourceTime(seg, props.time));
      }
      const live = sessionRef.current!;
      const frame = live.queue.takeUpTo(targetUs);
      if (frame) {
        live.lastPaintedUs = frame.timestamp;
        paintFrame(frame);
      }
      // Overhang (source shorter than the clip) freezes on the last painted
      // frame by construction: the queue simply stops producing.

      // Gapless boundaries: open the next segment's decoder ahead of time.
      const pre = prebufferTarget(props.segments, props.time, PREBUFFER_S);
      if (pre) void decoderFor(pre.sourceId).catch(() => {});
      return;
    }

    // Paused: one-shot decode of the exact frame; rapid scrubs abort the
    // superseded request. The play session is torn down — a paused seek
    // invalidates its forward-only position anyway.
    endSession();
    if (!seg) {
      scrubKeyRef.current = null;
      const c = canvasRef.current;
      if (c) c.getContext("2d")!.clearRect(0, 0, c.width, c.height);
      return;
    }
    const target = sourceTime(seg, props.time);
    // Dedupe: the effect fires on EVERY parent render (segments identity),
    // and re-decoding the same (source, frame) on drag-previews / autosave
    // status ticks is pure hardware-decoder churn.
    const scrubKey = `${seg.sourceId}:${Math.round(target * 1e6)}`;
    if (scrubKey === scrubKeyRef.current && !scrubAbortRef.current?.signal.aborted) return;
    scrubKeyRef.current = scrubKey;
    scrubAbortRef.current?.abort();
    const abort = new AbortController();
    scrubAbortRef.current = abort;
    void (async () => {
      try {
        const dec = await decoderFor(seg.sourceId);
        if (abort.signal.aborted) return;
        for await (const frame of dec.frames(target, abort.signal)) {
          // First yielded frame is the seek target (pre-roll is internal).
          if (!abort.signal.aborted) paintFrame(frame);
          else frame.close();
          break; // disposes the generator → finally closes the decoder run
        }
      } catch (e) {
        if (!abort.signal.aborted) propsRef.current.onError?.(`engine: ${String(e)}`);
      }
    })();
  }, [props.time, props.playing, props.segments]);

  // Unmount: kill everything (decoder cache holds no GPU frames; sessions do).
  // A grade change while PAUSED must repaint from the raw copy — no new
  // frame is coming to trigger paintFrame.
  useEffect(() => {
    if (!propsRef.current.playing) composite();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [props.grades]);

  useEffect(
    () => () => {
      endSession();
      scrubAbortRef.current?.abort();
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [],
  );

  return <canvas ref={canvasRef} className="tl-engine-canvas" />;
}
