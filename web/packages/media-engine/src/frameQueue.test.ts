import { describe, expect, it } from "vitest";
import { FrameQueue } from "./frameQueue";

const stub = (tUs: number, closed: number[]) => ({
  timestamp: tUs,
  close: () => closed.push(tUs),
});

describe("FrameQueue", () => {
  it("takeUpTo returns the latest due frame and closes the skipped ones", () => {
    const closed: number[] = [];
    const q = new FrameQueue<ReturnType<typeof stub>>(10);
    for (const t of [0, 40_000, 80_000, 120_000]) q.push(stub(t, closed));
    const f = q.takeUpTo(90_000);
    expect(f?.timestamp).toBe(80_000); // latest <= 90ms
    expect(closed).toEqual([0, 40_000]); // display window passed
    expect(q.size).toBe(1); // 120ms stays queued
  });

  it("returns null when nothing is due (caller keeps the previous paint)", () => {
    const closed: number[] = [];
    const q = new FrameQueue<ReturnType<typeof stub>>(10);
    q.push(stub(50_000, closed));
    expect(q.takeUpTo(10_000)).toBeNull();
    expect(q.size).toBe(1);
    expect(closed).toEqual([]);
  });

  it("evicts (and closes) the oldest frame past capacity", () => {
    const closed: number[] = [];
    const q = new FrameQueue<ReturnType<typeof stub>>(2);
    q.push(stub(1, closed));
    q.push(stub(2, closed));
    q.push(stub(3, closed));
    expect(closed).toEqual([1]);
    expect(q.size).toBe(2);
    expect(q.full).toBe(true);
  });

  it("clear closes everything", () => {
    const closed: number[] = [];
    const q = new FrameQueue<ReturnType<typeof stub>>(4);
    q.push(stub(1, closed));
    q.push(stub(2, closed));
    q.clear();
    expect(closed).toEqual([1, 2]);
    expect(q.size).toBe(0);
  });

  it("frames are handed out exactly once (ownership transfer)", () => {
    const closed: number[] = [];
    const q = new FrameQueue<ReturnType<typeof stub>>(4);
    q.push(stub(10, closed));
    const f = q.takeUpTo(10);
    expect(f?.timestamp).toBe(10);
    expect(q.takeUpTo(10)).toBeNull();
    expect(closed).toEqual([]); // taken, not closed — caller owns it
  });
});
