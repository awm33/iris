// Sample-table math, kept free of WebCodecs/mp4box so it unit-tests in
// vitest. Times are SECONDS (converted from the container timescale at
// index build time).

export interface SampleMeta {
  /** Presentation time, seconds. */
  t: number;
  /** Sample duration, seconds. */
  duration: number;
  /** Sync sample (keyframe) — a legal decode entry point. */
  sync: boolean;
}

/** Index of the sample decoding must start from to present time t:
 * the LAST sync sample at-or-before t. Before the first sync sample the
 * only legal entry is the first sample; past the end, the last sync. */
export function seekPointIndex(index: SampleMeta[], t: number): number {
  let point = 0;
  for (let i = 0; i < index.length; i++) {
    if (index[i].t > t) break;
    if (index[i].sync) point = i;
  }
  return point;
}

/** End of the indexed stream, seconds. */
export function streamEnd(index: SampleMeta[]): number {
  const last = index[index.length - 1];
  return last ? last.t + last.duration : 0;
}
