import { activeOps } from "./doc";
import { type CanvasOp, newOpId } from "./ops";

// Timeline op vocabulary (M5) — second consumer of the op-log runtime.
// Same envelope ({op_id, type}) and undo-as-op semantics as the canvas;
// only the op types differ. Times are SECONDS (fps lives on the timeline
// row for display/snapping).

export interface ClipInit {
  id: string;
  name: string;
  /** Media clip: a pinned asset version (video or audio). */
  version_id?: string;
  /** Shot clip: a placeholder bound to a story shot — the selected take
   * resolves to pixels at render/preview time (never stored here). */
  shot_id?: string;
  start: number; // timeline position, seconds
  duration: number; // seconds on the timeline
  in_point?: number; // source in, seconds (default 0)
}

export type TimelineOp =
  | { op_id: string; type: "add_track"; track: { id: string; kind: "video" | "audio"; name?: string }; index?: number }
  | { op_id: string; type: "remove_track"; track_id: string }
  | { op_id: string; type: "add_clip"; track_id: string; clip: ClipInit }
  | { op_id: string; type: "remove_clip"; clip_id: string }
  | {
      op_id: string;
      type: "move_clip";
      clip_id: string;
      start: number;
      track_id?: string; // cross-track move
    }
  | { op_id: string; type: "trim_clip"; clip_id: string; start?: number; duration?: number; in_point?: number }
  | { op_id: string; type: "undo"; target: string };

export interface ClipState {
  id: string;
  name: string;
  versionId?: string;
  shotId?: string;
  start: number;
  duration: number;
  inPoint: number;
}

export interface TrackState {
  id: string;
  kind: "video" | "audio";
  name: string;
  clips: ClipState[]; // kept sorted by start
}

export interface TimelineState {
  tracks: TrackState[];
}

export const MIN_CLIP_S = 0.04; // one 24fps frame — reducer floor, shared by editing helpers

export function reduceTimeline(ops: TimelineOp[]): TimelineState {
  const tracks: TrackState[] = [];
  const findTrack = (id: string) => tracks.find((t) => t.id === id);
  const findClip = (id: string): [TrackState, ClipState] | undefined => {
    for (const t of tracks) {
      const c = t.clips.find((c) => c.id === id);
      if (c) return [t, c];
    }
    return undefined;
  };
  const sort = (t: TrackState) => t.clips.sort((a, b) => a.start - b.start);

  // activeOps is generic over the envelope; the cast crosses vocabularies.
  for (const op of activeOps(ops as unknown as CanvasOp[]) as unknown as TimelineOp[]) {
    switch (op.type) {
      case "add_track": {
        if (findTrack(op.track.id)) break;
        const t: TrackState = {
          id: op.track.id,
          kind: op.track.kind,
          name: op.track.name ?? (op.track.kind === "video" ? "V" : "A"),
          clips: [],
        };
        const i = op.index === undefined ? tracks.length : Math.max(0, Math.min(op.index, tracks.length));
        tracks.splice(i, 0, t);
        break;
      }
      case "remove_track": {
        const i = tracks.findIndex((t) => t.id === op.track_id);
        if (i !== -1) tracks.splice(i, 1);
        break;
      }
      case "add_clip": {
        const t = findTrack(op.track_id);
        if (!t || findClip(op.clip.id)) break;
        t.clips.push({
          id: op.clip.id,
          name: op.clip.name,
          versionId: op.clip.version_id,
          shotId: op.clip.shot_id,
          start: Math.max(0, op.clip.start),
          duration: Math.max(MIN_CLIP_S, op.clip.duration),
          inPoint: Math.max(0, op.clip.in_point ?? 0),
        });
        sort(t);
        break;
      }
      case "remove_clip": {
        const hit = findClip(op.clip_id);
        if (hit) hit[0].clips.splice(hit[0].clips.indexOf(hit[1]), 1);
        break;
      }
      case "move_clip": {
        const hit = findClip(op.clip_id);
        if (!hit) break;
        const [from, clip] = hit;
        // Both-or-neither: a rejected cross-track move must not half-apply
        // by still changing start — op atomicity matters to non-UI authors.
        if (op.track_id && op.track_id !== from.id) {
          const to = findTrack(op.track_id);
          if (!to || to.kind !== from.kind) break;
          clip.start = Math.max(0, op.start);
          from.clips.splice(from.clips.indexOf(clip), 1);
          to.clips.push(clip);
          sort(to);
          break;
        }
        clip.start = Math.max(0, op.start);
        sort(from);
        break;
      }
      case "trim_clip": {
        const hit = findClip(op.clip_id);
        if (!hit) break;
        const clip = hit[1];
        if (op.start !== undefined) clip.start = Math.max(0, op.start);
        if (op.duration !== undefined) clip.duration = Math.max(MIN_CLIP_S, op.duration);
        if (op.in_point !== undefined) clip.inPoint = Math.max(0, op.in_point);
        sort(hit[0]);
        break;
      }
    }
  }
  return { tracks };
}

/** Timeline length = end of the last clip. */
export function timelineDuration(state: TimelineState): number {
  let end = 0;
  for (const t of state.tracks) {
    for (const c of t.clips) end = Math.max(end, c.start + c.duration);
  }
  return end;
}

/** Ops that split a clip at absolute time t: trim the original to the left
 * half, add the right half (in_point advanced so content stays put). Returns
 * null when t isn't strictly inside the clip or a side would collapse below
 * one frame. NOTE: a blade is two ops, so it takes two undos to fully revert
 * — op grouping is a future vocabulary change if that stings in practice. */
export function bladeOps(
  state: TimelineState,
  clipId: string,
  t: number,
  newClipId: string,
  // Shot clips have no version_id, but once a take is SELECTED they have a
  // real source — the caller passes that resolution knowledge so the right
  // half keeps source continuity instead of restarting the take at 0.
  hasSource?: boolean,
): TimelineOp[] | null {
  // Round the cut point once so both halves abut exactly and the raw
  // playhead float never reaches the persisted log.
  const rt = Math.round(t * 100) / 100;
  for (const track of state.tracks) {
    const clip = track.clips.find((c) => c.id === clipId);
    if (!clip) continue;
    const left = rt - clip.start;
    const right = clip.start + clip.duration - rt;
    if (left < MIN_CLIP_S || right < MIN_CLIP_S) return null;
    return [
      { op_id: newOpId(), type: "trim_clip", clip_id: clip.id, duration: left },
      {
        op_id: newOpId(),
        type: "add_clip",
        track_id: track.id,
        clip: {
          id: newClipId,
          name: clip.name,
          version_id: clip.versionId,
          shot_id: clip.shotId,
          start: rt,
          // Placeholders have no source: content anchoring is meaningless,
          // and a nonzero in_point would skew their left-trim clamp.
          duration: right,
          in_point: (hasSource ?? !!clip.versionId) ? clip.inPoint + left : 0,
        },
      },
    ];
  }
  return null;
}

/** Ops that shift every clip on a track starting at-or-after afterS by
 * deltaS — the ripple companion to a trim or delete. One move_clip per
 * shifted clip (multi-op gesture, like blade: one undo per op). The
 * epsilon absorbs the r2-rounded starts the UI commits. */
export function rippleOps(state: TimelineState, trackId: string, afterS: number, deltaS: number): TimelineOp[] {
  const track = state.tracks.find((t) => t.id === trackId);
  if (!track || deltaS === 0) return [];
  const matched = track.clips.filter((c) => c.start >= afterS - 0.005);
  if (matched.length === 0) return [];
  // Clamp the DELTA once (never each start): per-clip zero-clamping would
  // compress relative spacing asymmetrically — overlaps/reorders real NLE
  // ripple never produces. Shipped callers can't exceed the bound (a trim
  // or delete delta is bounded by the span before the pivot), so this is
  // hardening for future callers.
  const d = Math.max(deltaS, -Math.min(...matched.map((c) => c.start)));
  if (d === 0) return [];
  return matched.map((c) => ({
    op_id: newOpId(),
    type: "move_clip",
    clip_id: c.id,
    start: Math.round((c.start + d) * 100) / 100,
  }));
}

/** Snap t to the nearest clip edge or extra candidate (playhead, 0) within
 * thresholdS; returns null when nothing is close enough — an explicit miss,
 * so an exact hit (distance 0) is distinguishable from no target, which a
 * return-input-unchanged sentinel cannot express. excludeClipId keeps a
 * dragged clip from snapping to itself. */
export function snapTime(
  state: TimelineState,
  t: number,
  opts: { thresholdS: number; excludeClipId?: string; extra?: number[] },
): number | null {
  let best: number | null = null;
  let bestDist = opts.thresholdS;
  const consider = (cand: number) => {
    const d = Math.abs(cand - t);
    if (d <= bestDist) {
      best = cand;
      bestDist = d;
    }
  };
  for (const track of state.tracks) {
    for (const c of track.clips) {
      if (c.id === opts.excludeClipId) continue;
      consider(c.start);
      consider(c.start + c.duration);
    }
  }
  for (const cand of opts.extra ?? [0]) consider(cand);
  return best;
}

/** Topmost video clip covering time t (track order = priority). */
export function clipAt(state: TimelineState, t: number, kind: "video" | "audio" = "video"): ClipState | undefined {
  for (const track of state.tracks) {
    if (track.kind !== kind) continue;
    const c = track.clips.find((c) => t >= c.start && t < c.start + c.duration);
    if (c) return c;
  }
  return undefined;
}

/** TimelineDoc: same shell as CanvasDoc over the timeline reducer. */
export class TimelineDoc {
  ops: TimelineOp[];
  state: TimelineState;
  onLocalOp?: (op: TimelineOp) => void;
  private listeners = new Set<() => void>();
  private redoTargets: string[] = [];

  constructor(ops: TimelineOp[] = []) {
    const seen = new Set<string>();
    this.ops = ops.filter((op) => !seen.has(op.op_id) && (seen.add(op.op_id), true));
    this.state = reduceTimeline(this.ops);
  }

  subscribe(fn: () => void): () => void {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  private commit(op: TimelineOp) {
    this.ops.push(op);
    this.state = reduceTimeline(this.ops);
    this.onLocalOp?.(op);
    this.listeners.forEach((fn) => fn());
  }

  apply(op: TimelineOp) {
    this.redoTargets = [];
    this.commit(op);
  }

  applyRemote(ops: TimelineOp[]) {
    if (ops.length === 0) return;
    const known = new Set(this.ops.map((o) => o.op_id));
    const fresh = ops.filter((o) => !known.has(o.op_id) && (known.add(o.op_id), true));
    if (fresh.length === 0) return;
    this.ops.push(...fresh);
    this.state = reduceTimeline(this.ops);
    this.listeners.forEach((fn) => fn());
  }

  get canUndo(): boolean {
    return (activeOps(this.ops as unknown as CanvasOp[]) as unknown as TimelineOp[]).length > 0;
  }
  get canRedo(): boolean {
    return this.redoTargets.length > 0;
  }

  undo() {
    const active = activeOps(this.ops as unknown as CanvasOp[]) as unknown as TimelineOp[];
    const last = active[active.length - 1];
    if (!last) return;
    const op: TimelineOp = { op_id: newOpId(), type: "undo", target: last.op_id };
    this.redoTargets.push(op.op_id);
    this.commit(op);
  }

  redo() {
    const target = this.redoTargets.pop();
    if (!target) return;
    this.commit({ op_id: newOpId(), type: "undo", target });
  }
}


