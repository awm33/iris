import { describe, expect, it } from "vitest";
import { clipAt, reduceTimeline, TimelineDoc, type TimelineOp, timelineDuration } from "./timeline";

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
      { op_id: "m2", type: "move_clip", clip_id: "x", start: 1, track_id: "a1" },
      { op_id: "tr", type: "trim_clip", clip_id: "x", duration: 0 },
    ];
    const st = reduceTimeline(ops);
    expect(st.tracks[0].clips[0].start).toBe(1);
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
