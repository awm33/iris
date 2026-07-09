import { describe, expect, it } from "vitest";
import { buildIndex } from "./buildIndex";

// bf=2 decode-order stream at 40ms frames whose smallest cts is 80ms —
// the edit-list bias shape mp4box exposes.
const raw = [
  { timestampUs: 80_000, durationUs: 40_000, sync: true }, // I (presented first)
  { timestampUs: 200_000, durationUs: 40_000, sync: false }, // P
  { timestampUs: 120_000, durationUs: 40_000, sync: false }, // B
  { timestampUs: 160_000, durationUs: 40_000, sync: false }, // B
];

describe("buildIndex", () => {
  it("shifts presentation time so the first PRESENTED frame is 0", () => {
    const b = buildIndex(raw);
    expect(b.biasUs).toBe(80_000);
    expect(b.timestampsUs).toEqual([0, 120_000, 40_000, 80_000]);
    expect(b.index[0].t).toBe(0);
  });

  it("preserves decode order — only timestamps shift", () => {
    const b = buildIndex(raw);
    expect(b.index.map((s) => s.sync)).toEqual([true, false, false, false]);
    // non-monotonic t is expected (B-frames); position i still maps to input i
    expect(b.index[1].t).toBeCloseTo(0.12);
  });

  it("empty stream → zero bias, empty index", () => {
    const b = buildIndex([]);
    expect(b.biasUs).toBe(0);
    expect(b.index).toEqual([]);
  });
});
