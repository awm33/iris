import { activeOps } from "./doc";
import { type CanvasOp, newOpId } from "./ops";

// Timeline op vocabulary (M5) — second consumer of the op-log runtime.
// Same envelope ({op_id, type}) and undo-as-op semantics as the canvas;
// only the op types differ. Times are SECONDS (fps lives on the timeline
// row for display/snapping).

export type TrackKind = "video" | "audio" | "caption";

/** Per-clip color grade (M7). Neutral = {exposure: 0, contrast: 1, temp: 0}.
 * The value ranges are reducer-clamped; the MATH is fixed by parity with
 * the export's lutrgb pipeline — see docs/design plan PR 36. */
export interface ClipColor {
  exposure: number; // stops, -3..3
  contrast: number; // 0..2, 1 = neutral
  temp: number; // -1 (cool) .. 1 (warm), attenuation-only gains
}

/** OUT-transition of a clip (M7): the blend window is [clip end, end + D)
 * — the outgoing clip continues past its out point (freeze past EOF) while
 * the next same-track clip plays its head; timeline duration never changes. */
export interface ClipTransition {
  kind: "dissolve";
  duration: number; // seconds, clamped [0.1, 2]
}

export interface ClipInit {
  id: string;
  name: string;
  /** Media clip: a pinned asset version (video or audio). */
  version_id?: string;
  /** Shot clip: a placeholder bound to a story shot — the selected take
   * resolves to pixels at render/preview time (never stored here). */
  shot_id?: string;
  /** Caption clip: the text shown for the clip's span (caption tracks). */
  text?: string;
  /** Color grade (video clips; absent = neutral). */
  color?: ClipColor;
  /** OUT-transition into the next same-track clip (absent = hard cut). */
  transition?: ClipTransition;
  /** Speech clip (M7 ducking): non-speech audio ducks while it plays. */
  speech?: boolean;
  start: number; // timeline position, seconds
  duration: number; // seconds on the timeline
  in_point?: number; // source in, seconds (default 0)
}

export type TimelineOp =
  | { op_id: string; type: "add_track"; track: { id: string; kind: TrackKind; name?: string }; index?: number }
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
  | { op_id: string; type: "set_clip_text"; clip_id: string; text: string }
  | { op_id: string; type: "set_clip_color"; clip_id: string; color?: ClipColor }
  | { op_id: string; type: "set_clip_transition"; clip_id: string; transition?: ClipTransition }
  | { op_id: string; type: "set_clip_speech"; clip_id: string; speech?: boolean }
  | { op_id: string; type: "undo"; target: string };

export interface ClipState {
  id: string;
  name: string;
  versionId?: string;
  shotId?: string;
  text?: string;
  color?: ClipColor;
  transition?: ClipTransition;
  speech?: boolean;
  start: number;
  duration: number;
  inPoint: number;
}

/** Reducer clamp for color values — MUST match the Go port exactly.
 * Non-finite (JSON null/absent field) lands on the field's neutral. */
export function clampColor(c: ClipColor): ClipColor {
  const f = (v: number, neutral: number, lo: number, hi: number) =>
    Number.isFinite(v) ? Math.min(hi, Math.max(lo, v)) : neutral;
  return { exposure: f(c.exposure, 0, -3, 3), contrast: f(c.contrast, 1, 0, 2), temp: f(c.temp, 0, -1, 1) };
}

/** Reducer clamp for transitions — MUST match the Go port exactly. Only
 * dissolve exists in v1: any kind normalizes to it; duration clamps to
 * [0.1, 2] with 0.5 as the absent/junk default. */
export function clampTransition(t: ClipTransition): ClipTransition {
  const d = Number.isFinite(t.duration) ? Math.min(2, Math.max(0.1, t.duration)) : 0.5;
  return { kind: "dissolve", duration: d };
}

export interface TrackState {
  id: string;
  kind: TrackKind;
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
          // || not ??: an explicit "" also takes the default — the Go
          // reducer can't distinguish "" from absent, and parity wins.
          name: op.track.name || (op.track.kind === "video" ? "V" : op.track.kind === "caption" ? "C" : "A"),
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
          text: op.clip.text,
          // != null, not truthiness: Go's tolerant decode treats ANY
          // present non-null value (including 0/false/"") as set-with-
          // defaults — a falsy-scalar fork here would diverge reduced
          // state across clients (review PR37-M4).
          color: op.clip.color != null ? clampColor(op.clip.color) : undefined,
          transition: op.clip.transition != null ? clampTransition(op.clip.transition) : undefined,
          // === true, both ends: only JSON true sets; false/absent/junk
          // clear (Go: *bool nil-or-false — scalar junk hits the recorded
          // ParseOps strictness gap, same as start/text).
          speech: op.clip.speech === true || undefined,
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
      case "set_clip_text": {
        const hit = findClip(op.clip_id);
        if (hit) hit[1].text = op.text;
        break;
      }
      case "set_clip_color": {
        const hit = findClip(op.clip_id);
        if (hit) hit[1].color = op.color != null ? clampColor(op.color) : undefined;
        break;
      }
      case "set_clip_transition": {
        const hit = findClip(op.clip_id);
        if (hit) hit[1].transition = op.transition != null ? clampTransition(op.transition) : undefined;
        break;
      }
      case "set_clip_speech": {
        const hit = findClip(op.clip_id);
        if (hit) hit[1].speech = op.speech === true || undefined;
        break;
      }
    }
  }
  return { tracks };
}

/** Ducking (M7): deterministic gain automation, identical math on both
 * ends (the export builds volume expressions, the mixer builds WebAudio
 * ramps — from the SAME windows). Non-speech audio follows
 * g(t) = 1 − (1−DUCK_LEVEL)·coverage(t), where coverage is the merged
 * speech-span trapezoid with DUCK_RAMP_S linear attack/release. */
export const DUCK_LEVEL = 0.25;
export const DUCK_RAMP_S = 0.15;

export interface DuckWindow {
  start: number;
  end: number;
}

/** Merged union of speech-clip spans, clamped to [0, totalS). Every
 * speech-flagged clip counts — resolution/audio-stream presence is a
 * render concern; determinism across both ends is the contract here. */
export function duckWindows(state: TimelineState, totalS: number): DuckWindow[] {
  const spans: DuckWindow[] = [];
  for (const t of state.tracks) {
    for (const c of t.clips) {
      if (!c.speech || c.start >= totalS) continue;
      spans.push({ start: Math.max(0, c.start), end: Math.min(totalS, c.start + c.duration) });
    }
  }
  spans.sort((a, b) => a.start - b.start);
  const merged: DuckWindow[] = [];
  for (const sp of spans) {
    const last = merged[merged.length - 1];
    if (last && sp.start <= last.end + 1e-6) last.end = Math.max(last.end, sp.end);
    else merged.push({ ...sp });
  }
  return merged;
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
 * one frame. NOTE: a blade is two ops — three when the clip carries an
 * out-transition (the clear on the left half) — so a full revert takes as
 * many undos; op grouping is a future vocabulary change if that stings. */
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
          text: clip.text,
          color: clip.color,
          // The OUT edge moved to the right half — it inherits the
          // transition; the left half's new out is the cut (cleared below).
          transition: clip.transition,
          speech: clip.speech,
          start: rt,
          // Placeholders have no source: content anchoring is meaningless,
          // and a nonzero in_point would skew their left-trim clamp.
          duration: right,
          in_point: (hasSource ?? !!clip.versionId) ? clip.inPoint + left : 0,
        },
      },
      ...(clip.transition
        ? [{ op_id: newOpId(), type: "set_clip_transition", clip_id: clip.id } as TimelineOp]
        : []),
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
export function clipAt(state: TimelineState, t: number, kind: TrackKind = "video"): ClipState | undefined {
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

  /** Undo every ACTIVE op after the op at log index `index` — the history
   * panel's "revert". Implemented as repeated undo() so activity comes
   * from the reducer's own activeOps (an undone-then-redone op counts as
   * active) and every step lands on redoTargets: ⇧⌘Z walks the whole
   * revert back. */
  undoTo(index: number) {
    for (;;) {
      const active = activeOps(this.ops as unknown as CanvasOp[]) as unknown as TimelineOp[];
      const last = active[active.length - 1];
      if (!last) return;
      const i = this.ops.findIndex((o) => o.op_id === last.op_id);
      if (i <= index) return;
      this.undo();
    }
  }
}


