import { describe, expect, it } from "vitest";
import { nextStart, prebufferTarget, segmentAt, sourceTime, type Segment } from "./schedule";

const segs: Segment[] = [
  { sourceId: "a", startS: 1, durationS: 3, inPointS: 0.5 }, // 1..4
  { sourceId: "b", startS: 5, durationS: 2, inPointS: 0 }, // 5..7 (gap 4..5)
  { sourceId: "a", startS: 7, durationS: 1, inPointS: 2 }, // 7..8 (same source as first)
];

describe("segmentAt / sourceTime", () => {
  it("covers [start, start+duration) and maps source-local time", () => {
    const s = segmentAt(segs, 2)!;
    expect(s.sourceId).toBe("a");
    expect(sourceTime(s, 2)).toBeCloseTo(1.5); // 2 - 1 + 0.5
    expect(segmentAt(segs, 4.5)).toBeUndefined(); // gap
    expect(segmentAt(segs, 4)).toBeUndefined(); // end-exclusive
    expect(segmentAt(segs, 5)).toBeDefined(); // start-inclusive
  });
});

describe("nextStart", () => {
  it("earliest upcoming start, strictly after t", () => {
    expect(nextStart(segs, 0)?.startS).toBe(1);
    expect(nextStart(segs, 1)?.startS).toBe(5); // strict: the 1s start doesn't count at t=1
    expect(nextStart(segs, 6)?.startS).toBe(7);
    expect(nextStart(segs, 7.5)).toBeUndefined();
  });
});

describe("prebufferTarget", () => {
  it("upcoming segment inside the horizon", () => {
    expect(prebufferTarget(segs, 3.8, 1.5)?.sourceId).toBe("b"); // 5 - 3.8 = 1.2 ≤ 1.5
    expect(prebufferTarget(segs, 2.0, 1.5)).toBeUndefined(); // 3s away
  });

  it("no prebuffer when the next segment reuses the playing source", () => {
    // at 6.6, next is the 7s segment with sourceId "a" ≠ current "b" → prebuffer
    expect(prebufferTarget(segs, 6.6, 1.5)?.startS).toBe(7);
    // same-source continuation: b playing, next also b
    const cont: Segment[] = [
      { sourceId: "b", startS: 0, durationS: 2, inPointS: 0 },
      { sourceId: "b", startS: 2, durationS: 2, inPointS: 2 },
    ];
    expect(prebufferTarget(cont, 1.6, 1.5)).toBeUndefined();
  });

  it("prebuffers across a gap (nothing playing)", () => {
    expect(prebufferTarget(segs, 4.2, 1.5)?.sourceId).toBe("b");
  });
});
