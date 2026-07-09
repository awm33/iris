// Audio scheduling — pure math from segments to WebAudio start() plans so
// the clamp/offset arithmetic unit-tests in vitest. The mixer anchors
// whenS to an AudioContext time at play start; sources are scheduled
// statically (the audio clock is sample-accurate — no per-frame chasing).
import type { Segment } from "./schedule";

export interface AudioPlanEntry {
  sourceId: string;
  /** Seconds after the play anchor this entry starts. */
  whenS: number;
  /** Source-local start offset, seconds. */
  offsetS: number;
  /** Seconds of source to play. */
  durationS: number;
}

/** Plan playback of every segment overlapping [fromS, ∞): segments already
 * ended are dropped; a segment straddling fromS starts immediately with
 * its offset advanced; future segments start at their timeline distance. */
export function audioSchedule(segments: Segment[], fromS: number): AudioPlanEntry[] {
  const plan: AudioPlanEntry[] = [];
  for (const seg of segments) {
    const end = seg.startS + seg.durationS;
    if (end <= fromS) continue;
    const startsAhead = seg.startS >= fromS;
    const entry: AudioPlanEntry = {
      sourceId: seg.sourceId,
      whenS: startsAhead ? seg.startS - fromS : 0,
      offsetS: seg.inPointS + (startsAhead ? 0 : fromS - seg.startS),
      durationS: end - Math.max(seg.startS, fromS),
    };
    if (entry.durationS > 0) plan.push(entry);
  }
  return plan;
}
