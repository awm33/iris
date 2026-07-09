// Sample-table math, kept free of WebCodecs/mp4box so it unit-tests in
// vitest. Times are SECONDS (converted from the container timescale at
// index build time).
//
// The table is in DECODE order (mp4box delivery order) — with B-frames the
// presentation times are NON-monotonic, so nothing here may early-exit on
// t ordering. Chunks must reach VideoDecoder in exactly this order; only
// the timestamps carry presentation time.

export interface SampleMeta {
  /** Presentation time (cts), seconds. NON-monotonic across B-frames. */
  t: number;
  /** Sample duration, seconds. */
  duration: number;
  /** Sync sample (keyframe) — a legal decode entry point. */
  sync: boolean;
}

/** Decode index of the sample decoding must start from to present time t:
 * the sync sample with the GREATEST presentation time at-or-before t.
 * Before the first eligible sync sample, entry falls back to index 0. */
export function seekPointIndex(index: SampleMeta[], t: number): number {
  let point = 0;
  let bestT = -Infinity;
  for (let i = 0; i < index.length; i++) {
    const s = index[i];
    if (s.sync && s.t <= t && s.t > bestT) {
      point = i;
      bestT = s.t;
    }
  }
  return point;
}

/** End of the indexed stream, seconds: the max presentation end — the
 * LAST array entry is decode order, not the latest-presented sample. */
export function streamEnd(index: SampleMeta[]): number {
  let end = 0;
  for (const s of index) end = Math.max(end, s.t + s.duration);
  return end;
}
