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
    expect((ops[1] as { clip: { shot_id?: string } }).clip.shot_id).toBe("sh_1");
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

  it("snaps to the nearest clip edge within the threshold", () => {
    expect(snapTime(state, 4.9, { thresholdS: 0.2 })).toBe(5); // end of a
    expect(snapTime(state, 9.85, { thresholdS: 0.2 })).toBe(10); // start of b
    expect(snapTime(state, 7, { thresholdS: 0.2 })).toBe(7); // nothing close
  });

  it("ignores the excluded clip's own edges", () => {
    expect(snapTime(state, 2.1, { thresholdS: 0.2, excludeClipId: "a" })).toBe(2.1);
    expect(snapTime(state, 2.1, { thresholdS: 0.2 })).toBe(2);
  });

  it("considers extra candidates like the playhead instead of the default origin", () => {
    expect(snapTime(state, 6.9, { thresholdS: 0.2, extra: [7.05] })).toBe(7.05);
    expect(snapTime(state, 0.1, { thresholdS: 0.2 })).toBe(0); // default extra: origin
    expect(snapTime(state, 0.1, { thresholdS: 0.2, extra: [] })).toBe(0.1);
  });
});
