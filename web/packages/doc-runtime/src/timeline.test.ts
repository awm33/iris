import { describe, expect, it } from "vitest";
import { clipAt, reduceTimeline, TimelineDoc, type TimelineOp, timelineDuration, bladeOps, snapTime } from "./timeline";

const track = (id: string, kind: "video" | "audio" = "video"): TimelineOp => ({
  op_id: `t_${id}`, type: "add_track", track: { id, kind },
});
const clip = (id: string, trackId: string, start: number, dur: number): TimelineOp => ({
  op_id: `c_${id}`, type: "add_clip", track_id: trackId,
  clip: { id, name: id, version_id: `v_${id}`, start, duration: dur },
});

describe("reduceTimeline", () => {
  it("builds tracks and keeps clips sorted by start", () => {
    const st = reduceTimeline([track("v1"), clip("b", "v1", 5, 2), clip("a", "v1", 0, 3)]);
    expect(st.tracks[0].clips.map((c) => c.id)).toEqual(["a", "b"]);
    expect(timelineDuration(st)).toBe(7);
  });

  it("move/trim clamp and re-sort; cross-track moves respect kind", () => {
    const ops: TimelineOp[] = [
      track("v1"), track("a1", "audio"), clip("x", "v1", 0, 4),
      { op_id: "m1", type: "move_clip", clip_id: "x", start: -2 },
      { op_id: "m2", type: "move_clip", clip_id: "x", start: 9, track_id: "a1" }, // kind mismatch: NO effect (atomicity)
      { op_id: "tr", type: "trim_clip", clip_id: "x", duration: 0 },
    ];
    const st = reduceTimeline(ops);
    expect(st.tracks[0].clips[0].start).toBe(0); // m1 clamped to 0; m2 rejected whole
    expect(st.tracks[1].clips).toHaveLength(0);
    expect(st.tracks[0].clips[0].duration).toBeGreaterThan(0);
  });

  it("clipAt returns the topmost covering clip; undo restores removed clips", () => {
    const ops: TimelineOp[] = [track("v1"), clip("x", "v1", 0, 5),
      { op_id: "rm", type: "remove_clip", clip_id: "x" }];
    expect(clipAt(reduceTimeline(ops), 2)).toBeUndefined();
    ops.push({ op_id: "u", type: "undo", target: "rm" });
    expect(clipAt(reduceTimeline(ops), 2)?.id).toBe("x");
  });

  it("TimelineDoc undo/redo round-trips", () => {
    const doc = new TimelineDoc([track("v1")]);
    doc.apply(clip("x", "v1", 0, 5));
    expect(doc.state.tracks[0].clips).toHaveLength(1);
    doc.undo();
    expect(doc.state.tracks[0].clips).toHaveLength(0);
    doc.redo();
    expect(doc.state.tracks[0].clips).toHaveLength(1);
  });

  it("shot placeholder clips carry shot_id without a version", () => {
    const st = reduceTimeline([track("v1"), {
      op_id: "s1", type: "add_clip", track_id: "v1",
      clip: { id: "sc", name: "Shot 1", shot_id: "sht_1", start: 0, duration: 6 },
    }]);
    expect(st.tracks[0].clips[0].shotId).toBe("sht_1");
    expect(st.tracks[0].clips[0].versionId).toBeUndefined();
  });
});

describe("bladeOps", () => {
  const state = reduceTimeline([
    { op_id: "t1", type: "add_track", track: { id: "v1", kind: "video" } },
    {
      op_id: "c1",
      type: "add_clip",
      track_id: "v1",
      clip: { id: "x", name: "shot A", version_id: "ver_1", start: 2, duration: 6, in_point: 1.5 },
    },
  ]);

  it("splits into a trimmed left half and a content-anchored right half", () => {
    const ops = bladeOps(state, "x", 5, "x2")!;
    expect(ops).toHaveLength(2);
    expect(ops[0]).toMatchObject({ type: "trim_clip", clip_id: "x", duration: 3 });
    expect(ops[1]).toMatchObject({
      type: "add_clip",
      track_id: "v1",
      clip: { id: "x2", version_id: "ver_1", start: 5, duration: 3, in_point: 4.5 },
    });
    // applying the ops yields two abutting clips covering the original span
    const after = reduceTimeline([
      { op_id: "t1", type: "add_track", track: { id: "v1", kind: "video" } },
      {
        op_id: "c1",
        type: "add_clip",
        track_id: "v1",
        clip: { id: "x", name: "shot A", version_id: "ver_1", start: 2, duration: 6, in_point: 1.5 },
      },
      ...ops,
    ]);
    expect(after.tracks[0].clips.map((c) => [c.start, c.duration])).toEqual([
      [2, 3],
      [5, 3],
    ]);
  });

  it("keeps shot_id on both halves of a placeholder clip", () => {
    const shots = reduceTimeline([
      { op_id: "t1", type: "add_track", track: { id: "v1", kind: "video" } },
      { op_id: "c1", type: "add_clip", track_id: "v1", clip: { id: "p", name: "wide", shot_id: "sh_1", start: 0, duration: 4 } },
    ]);
    const ops = bladeOps(shots, "p", 1, "p2")!;
    const clip = (ops[1] as { clip: { shot_id?: string; in_point?: number } }).clip;
    expect(clip.shot_id).toBe("sh_1");
    // No source, nothing to anchor: a nonzero in_point would skew the
    // placeholder's left-trim clamp in the UI.
    expect(clip.in_point).toBe(0);
  });

  it("hasSource=true keeps in_point continuity for a resolved shot clip", () => {
    const shots = reduceTimeline([
      { op_id: "t1", type: "add_track", track: { id: "v1", kind: "video" } },
      { op_id: "c1", type: "add_clip", track_id: "v1", clip: { id: "p", name: "wide", shot_id: "sh_1", start: 0, duration: 4 } },
    ]);
    const ops = bladeOps(shots, "p", 1, "p2", true)!;
    const clip = (ops[1] as { clip: { shot_id?: string; in_point?: number } }).clip;
    expect(clip.shot_id).toBe("sh_1"); // shot binding survives
    expect(clip.in_point).toBe(1); // the selected take continues, not restarts
  });

  it("rounds the cut point so the halves abut without float noise", () => {
    const ops = bladeOps(state, "x", 4.333333333, "x2")!;
    expect(ops[0]).toMatchObject({ type: "trim_clip", duration: 2.33 });
    expect(ops[1]).toMatchObject({ type: "add_clip", clip: { start: 4.33, in_point: 3.83 } });
  });

  it("rejects cuts outside the clip or within a frame of an edge", () => {
    expect(bladeOps(state, "x", 2, "n")).toBeNull(); // at left edge
    expect(bladeOps(state, "x", 8, "n")).toBeNull(); // at right edge
    expect(bladeOps(state, "x", 1, "n")).toBeNull(); // before clip
    expect(bladeOps(state, "x", 2.01, "n")).toBeNull(); // sub-frame left sliver
    expect(bladeOps(state, "missing", 5, "n")).toBeNull();
  });
});

describe("snapTime", () => {
  const state = reduceTimeline([
    { op_id: "t1", type: "add_track", track: { id: "v1", kind: "video" } },
    { op_id: "c1", type: "add_clip", track_id: "v1", clip: { id: "a", name: "a", start: 2, duration: 3 } },
    { op_id: "c2", type: "add_clip", track_id: "v1", clip: { id: "b", name: "b", start: 10, duration: 2 } },
  ]);

  it("snaps to the nearest clip edge within the threshold, null when nothing is close", () => {
    expect(snapTime(state, 4.9, { thresholdS: 0.2 })).toBe(5); // end of a
    expect(snapTime(state, 9.85, { thresholdS: 0.2 })).toBe(10); // start of b
    expect(snapTime(state, 7, { thresholdS: 0.2 })).toBeNull(); // nothing close
  });

  it("reports an exact hit as a hit, not a miss", () => {
    // A distance-0 candidate must be distinguishable from "no target" —
    // callers comparing edges would otherwise let an unsnapped edge win.
    expect(snapTime(state, 5, { thresholdS: 0.2 })).toBe(5);
    expect(snapTime(state, 0, { thresholdS: 0.2 })).toBe(0); // origin extra, exact
  });

  it("ignores the excluded clip's own edges", () => {
    expect(snapTime(state, 2.1, { thresholdS: 0.2, excludeClipId: "a" })).toBeNull();
    expect(snapTime(state, 2.1, { thresholdS: 0.2 })).toBe(2);
  });

  it("considers extra candidates like the playhead instead of the default origin", () => {
    expect(snapTime(state, 6.9, { thresholdS: 0.2, extra: [7.05] })).toBe(7.05);
    expect(snapTime(state, 0.1, { thresholdS: 0.2 })).toBe(0); // default extra: origin
    expect(snapTime(state, 0.1, { thresholdS: 0.2, extra: [] })).toBeNull();
  });
});

describe("blade + undo interaction", () => {
  it("two undos fully revert a blade (add first, then trim)", () => {
    const doc = new TimelineDoc([
      { op_id: "t1", type: "add_track", track: { id: "v1", kind: "video" } },
      { op_id: "c1", type: "add_clip", track_id: "v1", clip: { id: "x", name: "a", start: 2, duration: 6, in_point: 1 } },
    ]);
    for (const op of bladeOps(doc.state, "x", 5, "x2")!) doc.apply(op);
    expect(doc.state.tracks[0].clips).toHaveLength(2);
    doc.undo(); // removes the add (right half disappears)
    expect(doc.state.tracks[0].clips).toHaveLength(1);
    expect(doc.state.tracks[0].clips[0].duration).toBe(3); // still trimmed
    doc.undo(); // removes the trim
    expect(doc.state.tracks[0].clips[0]).toMatchObject({ start: 2, duration: 6, inPoint: 1 });
    doc.redo();
    doc.redo();
    expect(doc.state.tracks[0].clips).toHaveLength(2);
  });
});

describe("reduce: combined trim_clip", () => {
  it("applies start + duration + in_point together (the left-trim op shape)", () => {
    const st = reduceTimeline([
      { op_id: "t1", type: "add_track", track: { id: "v1", kind: "video" } },
      { op_id: "c1", type: "add_clip", track_id: "v1", clip: { id: "x", name: "a", start: 2, duration: 6, in_point: 1 } },
      { op_id: "tr", type: "trim_clip", clip_id: "x", start: 3.5, duration: 4.5, in_point: 2.5 },
    ]);
    expect(st.tracks[0].clips[0]).toMatchObject({ start: 3.5, duration: 4.5, inPoint: 2.5 });
  });
});
