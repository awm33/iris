import { describe, expect, it } from "vitest";
import { type SampleMeta, seekPointIndex, streamEnd } from "./sampleIndex";

// 0.5s GOP at 4fps: sync at 0, 0.5, 1.0 …
const gop = (n: number): SampleMeta[] =>
  Array.from({ length: n }, (_, i) => ({ t: i * 0.25, duration: 0.25, sync: i % 2 === 0 }));

describe("seekPointIndex", () => {
  const index = gop(8); // t: 0 .25 .5 .75 1.0 1.25 1.5 1.75; sync on even indices

  it("picks the last sync sample at or before t", () => {
    expect(seekPointIndex(index, 0.6)).toBe(2); // sync at 0.5
    expect(seekPointIndex(index, 1.3)).toBe(4); // sync at 1.0
  });

  it("exact sync hit is its own entry point", () => {
    expect(seekPointIndex(index, 0.5)).toBe(2);
  });

  it("a non-sync exact hit still enters at the earlier sync", () => {
    expect(seekPointIndex(index, 0.75)).toBe(2);
  });

  it("before the first sample enters at 0; past the end enters at the last sync", () => {
    expect(seekPointIndex(index, -1)).toBe(0);
    expect(seekPointIndex(index, 99)).toBe(6); // sync at 1.5
  });

  it("open-GOP-ish tables (first sample not marked sync) fall back to 0", () => {
    const odd = gop(4).map((s, i) => ({ ...s, sync: i === 3 }));
    expect(seekPointIndex(odd, 0.3)).toBe(0);
  });
});

describe("decode-order tables (B-frames: presentation time is non-monotonic)", () => {
  // x264 bf=2 shape at 0.25s frames — decode order I P B B P B B …
  // cts:                                0  .75 .25 .5  1.5 1.0 1.25
  const bframes: SampleMeta[] = [
    { t: 0, duration: 0.25, sync: true },
    { t: 0.75, duration: 0.25, sync: false },
    { t: 0.25, duration: 0.25, sync: false },
    { t: 0.5, duration: 0.25, sync: false },
    { t: 1.75, duration: 0.25, sync: true }, // next GOP's IDR
    { t: 1.0, duration: 0.25, sync: false },
    { t: 1.25, duration: 0.25, sync: false },
    { t: 1.5, duration: 0.25, sync: false },
  ];

  it("seek picks the sync sample with the greatest cts ≤ t, not the last scanned", () => {
    expect(seekPointIndex(bframes, 0.6)).toBe(0); // only IDR at 0 qualifies
    expect(seekPointIndex(bframes, 1.75)).toBe(4); // second IDR, mid-array
    expect(seekPointIndex(bframes, 99)).toBe(4);
  });

  it("no early exit: a later array entry with smaller cts must not stop the scan", () => {
    // target 1.9 — the qualifying IDR (t=1.75) sits BEFORE smaller-cts B frames
    expect(seekPointIndex(bframes, 1.9)).toBe(4);
  });

  it("streamEnd is the max presentation end, not the last entry's", () => {
    expect(streamEnd(bframes)).toBeCloseTo(2.0); // 1.75+0.25, held mid-array
  });
});

describe("streamEnd", () => {
  it("is the last sample's end", () => {
    expect(streamEnd(gop(8))).toBeCloseTo(2.0);
    expect(streamEnd([])).toBe(0);
  });
});
