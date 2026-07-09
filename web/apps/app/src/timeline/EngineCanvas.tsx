import { useEffect, useRef } from "react";
import {
  ClipDecoder,
  FrameQueue,
  prebufferTarget,
  type Segment,
  segmentAt,
  sourceTime,
} from "@iris/media-engine";

// Timeline compositor v1 (PR 23): the media engine paints resolved video
// segments to one canvas, chasing the page's wall-clock playhead. Gapless
// boundaries come from prebuffering the next segment's decoder inside
// PREBUFFER_S. Silent by design until the audio slice (PR 24) — the
// engine preview ships behind a toolbar toggle until then.
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
  /** Resolve a segment source to a fetchable (signed) URL. */
  srcFor: (sourceId: string) => Promise<string>;
  onError?: (message: string) => void;
}) {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const decodersRef = useRef(new Map<string, Promise<ClipDecoder>>());
  const sessionRef = useRef<PlaySession | null>(null);
  const scrubAbortRef = useRef<AbortController | null>(null);
  const propsRef = useRef(props);
  propsRef.current = props;

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
    opened.catch(() => cache.delete(sourceId)); // failed opens must retry next time
    cache.set(sourceId, opened);
    while (cache.size > MAX_DECODERS) {
      const oldest = cache.keys().next().value!;
      if (oldest === sessionRef.current?.sourceId) break; // never evict the playing source
      cache.delete(oldest);
    }
    return opened;
  };

  const endSession = () => {
    sessionRef.current?.abort.abort();
    sessionRef.current?.queue.clear();
    sessionRef.current = null;
  };

  const paintFrame = (frame: VideoFrame) => {
    const canvas = canvasRef.current;
    if (!canvas) {
      frame.close();
      return;
    }
    if (canvas.width !== frame.displayWidth || canvas.height !== frame.displayHeight) {
      canvas.width = frame.displayWidth;
      canvas.height = frame.displayHeight;
    }
    canvas.getContext("2d")!.drawImage(frame, 0, 0);
    frame.close();
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
        if (!abort.signal.aborted) propsRef.current.onError?.(`engine: ${String(e)}`);
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
        // blanks the canvas (placeholder overlays render above us).
        endSession();
        const c = canvasRef.current;
        if (c) c.getContext("2d")!.clearRect(0, 0, c.width, c.height);
        return;
      }
      const targetUs = Math.round(sourceTime(seg, props.time) * 1e6);
      const s = sessionRef.current;
      // (Re)start on: source switch, or a backward jump the decode-forward
      // generator can't serve (scrub-while-playing restarts playback).
      if (!s || s.sourceId !== seg.sourceId || targetUs < s.lastPaintedUs - 250_000) {
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
      const c = canvasRef.current;
      if (c) c.getContext("2d")!.clearRect(0, 0, c.width, c.height);
      return;
    }
    scrubAbortRef.current?.abort();
    const abort = new AbortController();
    scrubAbortRef.current = abort;
    const target = sourceTime(seg, props.time);
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
