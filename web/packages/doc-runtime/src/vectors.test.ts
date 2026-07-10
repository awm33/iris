import { readdirSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { TimelineDoc, type TimelineOp, type TimelineState } from "./timeline";

// Shared drift vectors: the same files replay through the Go reducer
// (backend/internal/timeline/vectors_test.go) that the export service
// renders from. A change to either reducer that fails here is
// preview/export divergence — fix the port, don't touch the vector.
//
// Replayed through TimelineDoc, NOT bare reduceTimeline: the doc dedups
// op_ids before reducing (as does Go's ParseOps), and the dedup is part of
// the semantics the vectors pin — see dedup-undo-edges.json.

const dir = fileURLToPath(new URL("../../../../spec/timeline-vectors/", import.meta.url));

function normalize(st: TimelineState) {
  return st.tracks.map((t) => ({
    id: t.id,
    kind: t.kind,
    name: t.name,
    clips: t.clips.map((c) => ({
      id: c.id,
      start: c.start,
      duration: c.duration,
      in_point: c.inPoint,
      version_id: c.versionId ?? "",
      shot_id: c.shotId ?? "",
      text: c.text ?? "",
    })),
  }));
}

describe("shared timeline vectors (TS ↔ Go reducer parity)", () => {
  const files = readdirSync(dir).filter((f) => f.endsWith(".json"));
  expect(files.length).toBeGreaterThan(0);
  for (const f of files) {
    const vec = JSON.parse(readFileSync(dir + f, "utf8")) as {
      name: string;
      ops: TimelineOp[];
      expected: { tracks: unknown };
    };
    it(`${f}: ${vec.name}`, () => {
      expect(normalize(new TimelineDoc(vec.ops).state)).toEqual(vec.expected.tracks);
    });
  }
});
