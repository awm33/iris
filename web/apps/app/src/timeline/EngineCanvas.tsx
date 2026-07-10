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

/** A dissolve window (M7 transitions): fromSourceId fades out over the
 * main segment's content (no main segment = fade to black) during
 * [start, start+duration); its source time at t is fromSeekS+(t−start). */
export interface BlendWindow {
  start: number;
  duration: number;
  fromSourceId: string;
  fromClipId?: string;
  fromSeekS: number;
  toClipId?: string;
}

export function EngineCanvas(props: {
  segments: Segment[];
  time: number;
  playing: boolean;
  /** Per-clip color grades (incl. any in-flight slider draft) — kept OFF
   * the segments array so a grade tick never respawns decode sessions. */
  grades?: Map<string, { exposure: number; contrast: number; temp: number }>;
  /** Dissolve windows, same identity discipline as grades. */
  blends?: BlendWindow[];
  /** Resolve a segment source to a fetchable (signed) URL. */
  srcFor: (sourceId: string) => Promise<string>;
  onError?: (message: string) => void;
}) {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  // Raw (ungraded) copies of the last painted frames: VideoFrames must
  // close promptly, but a paused grade change still needs pixels to
  // recomposite. rawFrom holds the OUTGOING layer during a dissolve.
  const rawRef = useRef<HTMLCanvasElement | null>(null);
  const rawFromRef = useRef<HTMLCanvasElement | null>(null);
  const scratchRef = useRef<HTMLCanvasElement | null>(null);
  const fromSessionRef = useRef<PlaySession | null>(null);
  const scrubFromAbortRef = useRef<AbortController | null>(null);
  const scrubFromKeyRef = useRef<string | null>(null);
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
  const endFromSession = () => {
    fromSessionRef.current?.abort.abort();
    fromSessionRef.current?.queue.clear();
    fromSessionRef.current = null;
  };

  const paintTo = (ref: { current: HTMLCanvasElement | null }, frame: VideoFrame) => {
    const raw = (ref.current ??= document.createElement("canvas"));
    if (raw.width !== frame.displayWidth || raw.height !== frame.displayHeight) {
      raw.width = frame.displayWidth;
      raw.height = frame.displayHeight;
    }
    raw.getContext("2d")!.drawImage(frame, 0, 0);
    frame.close();
    composite();
  };
  const paintFrame = (frame: VideoFrame) => paintTo(rawRef, frame);
  const paintFromFrame = (frame: VideoFrame) => paintTo(rawFromRef, frame);

  const activeBlendAt = (t: number) =>
    propsRef.current.blends?.find((b) => t >= b.start && t < b.start + b.duration);

  // gradeInto draws src into dst applying a clip grade — the EXPORT's
  // lutrgb pipeline to within ~1-2 LSB (plan PR 36): brightness(2^stops)
  // then contrast(), each clamped, per channel, sRGB; then an
  // attenuation-only per-channel multiply for temperature (gains ≤ 1 by
  // construction; a multiply blend cannot brighten).
  const gradeInto = (
    dst: CanvasRenderingContext2D,
    src: HTMLCanvasElement,
    grade: { exposure: number; contrast: number; temp: number } | undefined,
    alpha: number,
  ) => {
    dst.save();
    dst.globalAlpha = alpha;
    if (grade && (grade.exposure !== 0 || grade.contrast !== 1)) {
      dst.filter = `brightness(${2 ** grade.exposure}) contrast(${grade.contrast})`;
    }
    if (grade && grade.temp !== 0) {
      // Temp needs a per-LAYER multiply — do it on a scratch canvas so the
      // multiply can't bleed into the layer below during a dissolve.
      const scratch = (scratchRef.current ??= document.createElement("canvas"));
      if (scratch.width !== src.width || scratch.height !== src.height) {
        scratch.width = src.width;
        scratch.height = src.height;
      }
      const sctx = scratch.getContext("2d")!;
      sctx.filter = dst.filter;
      sctx.drawImage(src, 0, 0);
      sctx.filter = "none";
      const t = grade.temp;
      const rG = t < 0 ? 1 + 0.3 * t : 1;
      const gG = t > 0 ? 1 - 0.1 * t : 1 + 0.1 * t;
      const bG = t > 0 ? 1 - 0.3 * t : 1;
      sctx.save();
      sctx.globalCompositeOperation = "multiply";
      sctx.fillStyle = `rgb(${Math.round(255 * rG)}, ${Math.round(255 * gG)}, ${Math.round(255 * bG)})`;
      sctx.fillRect(0, 0, scratch.width, scratch.height);
      sctx.restore();
      dst.filter = "none";
      dst.drawImage(scratch, 0, 0);
    } else {
      dst.drawImage(src, 0, 0);
      dst.filter = "none";
    }
    dst.restore();
  };

  // composite renders raw layer(s) → visible with grades and any active
  // dissolve. Dissolve math mirrors the export's xfade fade exactly:
  // out = A·(1−p) + B·p with p linear over the window — draw the outgoing
  // fully, then the incoming at globalAlpha=p (or black base for a fade
  // into a gap).
  const composite = () => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const { segments, time, grades } = propsRef.current;
    const seg = segmentAt(segments, time);
    const blend = activeBlendAt(time);
    const raw = rawRef.current;
    const rawFrom = rawFromRef.current;
    const ctx = canvas.getContext("2d")!;
    if (!seg && !blend) {
      // Stale raw pixels must not recomposite into a gap (grade-change
      // repaint while the playhead sits on no clip).
      ctx.clearRect(0, 0, canvas.width, canvas.height);
      return;
    }
    const size = seg && raw && raw.width > 0 ? raw : rawFrom;
    if (!size || size.width === 0) return;
    if (canvas.width !== size.width || canvas.height !== size.height) {
      canvas.width = size.width;
      canvas.height = size.height;
    }
    const gradeOf = (clipId?: string) => (clipId ? grades?.get(clipId) : undefined);
    if (blend && rawFrom && rawFrom.width > 0) {
      const p = Math.min(1, Math.max(0, (time - blend.start) / blend.duration));
      ctx.fillStyle = "#000";
      ctx.fillRect(0, 0, canvas.width, canvas.height);
      if (seg && raw && raw.width > 0) {
        // xfade's fade: out = A·(1−p) + B·p. Source-over gives exactly
        // that with A at alpha 1 then B at alpha p (opaque layers). A at
        // (1−p) then B at p would square the outgoing term.
        gradeInto(ctx, rawFrom, gradeOf(blend.fromClipId), 1);
        gradeInto(ctx, raw, gradeOf(seg.clipId), p);
      } else {
        // Fade into a gap: black base, outgoing at (1−p).
        gradeInto(ctx, rawFrom, gradeOf(blend.fromClipId), 1 - p);
      }
      return;
    }
    if (!seg || !raw || raw.width === 0) {
      ctx.clearRect(0, 0, canvas.width, canvas.height);
      return;
    }
    gradeInto(ctx, raw, gradeOf(seg.clipId), 1);
  };

  const startSessionIn = (holder: { current: PlaySession | null }, sourceId: string, fromS: number) => {
    holder.current?.abort.abort();
    holder.current?.queue.clear();
    const abort = new AbortController();
    const session: PlaySession = {
      sourceId,
      queue: new FrameQueue<VideoFrame>(3),
      abort,
      lastPaintedUs: Math.round(fromS * 1e6),
      done: false,
    };
    holder.current = session;
    void (async () => {
      const dec = await decoderFor(sourceId);
      if (abort.signal.aborted) return;
      // Clamp to one frame before EOF: a seek at/past the end yields NO
      // frames at all — a dissolve's outgoing side (continuing past its
      // out point) and clips outrunning short takes must FREEZE on the
      // last frame instead (the export's tpad/-ss-clamp behavior).
      const startS = Math.min(fromS, Math.max(0, dec.endS - 1 / 24));
      for await (const frame of dec.frames(startS, abort.signal)) {
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
          if (holder.current === session) {
            holder.current = null;
            session.abort.abort();
            session.queue.clear();
          }
        }
      })
      .finally(() => {
        session.done = true;
      });
  };
  const startSession = (sourceId: string, fromS: number) => startSessionIn(sessionRef, sourceId, fromS);

  // Playback painting: chase the playhead each time tick.
  useEffect(() => {
    const seg = segmentAt(props.segments, props.time);
    if (props.playing) {
      scrubAbortRef.current?.abort();
      if (!seg) {
        // Gap — but a fade-to-black window can live here: keep chasing
        // the outgoing layer and let composite() ramp it out.
        const blend = activeBlendAt(props.time);
        if (blend) {
          const fromTargetS = blend.fromSeekS + (props.time - blend.start);
          const fromTargetUs = Math.round(fromTargetS * 1e6);
          const fs = fromSessionRef.current;
          if (!fs || fs.sourceId !== blend.fromSourceId || fromTargetUs < fs.lastPaintedUs - 250_000) {
            startSessionIn(fromSessionRef, blend.fromSourceId, fromTargetS);
          }
          const ff = fromSessionRef.current?.queue.takeUpTo(fromTargetUs);
          if (ff && fromSessionRef.current) {
            fromSessionRef.current.lastPaintedUs = ff.timestamp;
            paintFromFrame(ff);
          } else {
            composite(); // ramp the alpha even between decoded frames
          }
          endSession();
          const pre = prebufferTarget(props.segments, props.time, PREBUFFER_S);
          if (pre) void decoderFor(pre.sourceId).catch(() => {});
          return;
        }
        endFromSession();
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
      // Dissolve window: run a SECOND session for the outgoing clip,
      // continuing past its out point (the decoder freezes at EOF, which
      // is the export's tpad behavior). Same restart rules, chased on the
      // same tick.
      const blend = activeBlendAt(props.time);
      if (blend) {
        const fromTargetS = blend.fromSeekS + (props.time - blend.start);
        const fromTargetUs = Math.round(fromTargetS * 1e6);
        const fs = fromSessionRef.current;
        if (!fs || fs.sourceId !== blend.fromSourceId || fromTargetUs < fs.lastPaintedUs - 250_000) {
          startSessionIn(fromSessionRef, blend.fromSourceId, fromTargetS);
        }
        const ff = fromSessionRef.current?.queue.takeUpTo(fromTargetUs);
        if (ff && fromSessionRef.current) {
          fromSessionRef.current.lastPaintedUs = ff.timestamp;
          paintFromFrame(ff);
        }
      } else if (fromSessionRef.current) {
        endFromSession();
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
    endFromSession();
    // A paused seek inside a dissolve window also one-shots the OUTGOING
    // frame into its own raw layer; composite blends the pair.
    const blend = activeBlendAt(props.time);
    if (blend) {
      const fromTarget = blend.fromSeekS + (props.time - blend.start);
      const key = `${blend.fromSourceId}:${Math.round(fromTarget * 1e6)}`;
      if (key !== scrubFromKeyRef.current || scrubFromAbortRef.current?.signal.aborted) {
        scrubFromKeyRef.current = key;
        scrubFromAbortRef.current?.abort();
        const abort = new AbortController();
        scrubFromAbortRef.current = abort;
        void (async () => {
          try {
            const dec = await decoderFor(blend.fromSourceId);
            if (abort.signal.aborted) return;
            const t = Math.min(fromTarget, Math.max(0, dec.endS - 1 / 24));
            for await (const frame of dec.frames(t, abort.signal)) {
              if (!abort.signal.aborted) paintFromFrame(frame);
              else frame.close();
              break;
            }
          } catch (e) {
            if (!abort.signal.aborted) propsRef.current.onError?.(`engine: ${String(e)}`);
          }
        })();
      }
    } else {
      scrubFromKeyRef.current = null;
      scrubFromAbortRef.current?.abort();
    }
    if (!seg) {
      scrubKeyRef.current = null;
      const c = canvasRef.current;
      if (c && !blend) c.getContext("2d")!.clearRect(0, 0, c.width, c.height);
      if (blend) composite(); // fade-to-black window over a gap
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
        const t = Math.min(target, Math.max(0, dec.endS - 1 / 24));
        for await (const frame of dec.frames(t, abort.signal)) {
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
      endFromSession();
      scrubAbortRef.current?.abort();
      scrubFromAbortRef.current?.abort();
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [],
  );

  return <canvas ref={canvasRef} className="tl-engine-canvas" />;
}
