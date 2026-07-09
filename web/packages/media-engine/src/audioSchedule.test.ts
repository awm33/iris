import { describe, expect, it } from "vitest";
import { audioSchedule } from "./audioSchedule";
import type { Segment } from "./schedule";

const segs: Segment[] = [
  { sourceId: "a", startS: 1, durationS: 3, inPointS: 0.5 }, // 1..4
  { sourceId: "b", startS: 5, durationS: 2, inPointS: 0 }, // 5..7
];

describe("audioSchedule", () => {
  it("from 0: both segments scheduled at their timeline distance", () => {
    expect(audioSchedule(segs, 0)).toEqual([
      { sourceId: "a", whenS: 1, offsetS: 0.5, durationS: 3 },
      { sourceId: "b", whenS: 5, offsetS: 0, durationS: 2 },
    ]);
  });

  it("mid-segment start: immediate, offset advanced, duration clamped", () => {
    expect(audioSchedule(segs, 2.5)).toEqual([
      { sourceId: "a", whenS: 0, offsetS: 2.0, durationS: 1.5 }, // 0.5 + (2.5-1)
      { sourceId: "b", whenS: 2.5, offsetS: 0, durationS: 2 },
    ]);
  });

  it("segments already ended are dropped; exact end boundary counts as ended", () => {
    expect(audioSchedule(segs, 4)).toEqual([{ sourceId: "b", whenS: 1, offsetS: 0, durationS: 2 }]);
    expect(audioSchedule(segs, 7)).toEqual([]);
  });

  it("start exactly at a segment start plays it whole with no offset shift", () => {
    expect(audioSchedule(segs, 5)).toEqual([{ sourceId: "b", whenS: 0, offsetS: 0, durationS: 2 }]);
  });
});
