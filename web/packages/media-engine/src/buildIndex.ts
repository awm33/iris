// Pure index construction over demuxed samples (decode order preserved),
// split from the WebCodecs shell so the edit-list normalization and
// ordering invariants unit-test in vitest.
import type { SampleMeta } from "./sampleIndex";

export interface RawSample {
  /** Presentation timestamp (cts), µs, container-native. */
  timestampUs: number;
  durationUs: number;
  sync: boolean;
}

export interface BuiltIndex {
  /** Per-sample presentation timestamps with the bias removed, µs,
   * decode order (parallel to the input array). */
  timestampsUs: number[];
  index: SampleMeta[];
  /** The subtracted bias, µs (min presentation time across the stream). */
  biasUs: number;
}

/** Normalize presentation time so the first PRESENTED frame is t=0.
 * mp4box's cts ignores edit lists; with B-frames the smallest cts is
 * ~2 frame durations above zero, which would skew every clock/seek by
 * that constant. Decode order is preserved — only timestamps shift. */
export function buildIndex(samples: RawSample[]): BuiltIndex {
  let biasUs = Infinity;
  for (const s of samples) biasUs = Math.min(biasUs, s.timestampUs);
  if (!Number.isFinite(biasUs)) biasUs = 0;
  const timestampsUs = samples.map((s) => s.timestampUs - biasUs);
  const index: SampleMeta[] = samples.map((s, i) => ({
    t: timestampsUs[i] / 1e6,
    duration: s.durationUs / 1e6,
    sync: s.sync,
  }));
  return { timestampsUs, index, biasUs };
}
