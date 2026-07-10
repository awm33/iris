// Sequence scheduling for the timeline compositor — pure math over
// segments so boundary/prebuffer decisions unit-test in vitest. A segment
// is a resolved, engine-playable span (the app maps clips → segments;
// gaps and placeholders simply have no segment).

export interface Segment {
  /** Opaque source identity (version id) — decoder cache key. */
  sourceId: string;
  /** Owning clip id — lets the compositor look up per-clip presentation
   * (color grades) WITHOUT it living here: segment identity keys decode
   * sessions, and presentation must never respawn a decoder. */
  clipId?: string;
  /** Timeline position, seconds. */
  startS: number;
  durationS: number;
  /** Source in-point, seconds. */
  inPointS: number;
  /** Speech segment (ducking): exempt from duck-gain automation. */
  speech?: boolean;
}

/** The segment covering timeline time t (first match wins — the app
 * passes segments in track-priority order). */
export function segmentAt(segments: Segment[], t: number): Segment | undefined {
  return segments.find((s) => t >= s.startS && t < s.startS + s.durationS);
}

/** Source-local time for a timeline time inside a segment. */
export function sourceTime(seg: Segment, t: number): number {
  return Math.max(0, t - seg.startS + seg.inPointS);
}

/** The next segment START strictly after t (boundaries where the
 * compositor must have the next decoder ready). Gaps and overlaps both
 * reduce to "earliest upcoming start". */
export function nextStart(segments: Segment[], t: number): Segment | undefined {
  let best: Segment | undefined;
  for (const s of segments) {
    if (s.startS > t && (!best || s.startS < best.startS)) best = s;
  }
  return best;
}

/** What to prebuffer at time t: the upcoming segment when its start is
 * within horizonS — undefined while it's still far away or when the
 * upcoming segment is the one already playing (same source continuing
 * needs no new decoder). */
export function prebufferTarget(segments: Segment[], t: number, horizonS: number): Segment | undefined {
  const current = segmentAt(segments, t);
  const next = nextStart(segments, t);
  if (!next || next.startS - t > horizonS) return undefined;
  if (current && current.sourceId === next.sourceId) return undefined;
  return next;
}
