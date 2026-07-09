import { describe, expect, it } from "vitest";
import { activeOps, CanvasDoc, reduce } from "./doc";
import type { CanvasOp } from "./ops";

const addLayer = (id: string, kind: "paint" | "image" = "paint", index?: number): CanvasOp => ({
  op_id: `add_${id}`,
  type: "add_layer",
  layer: { id, name: id, kind },
  index,
});
const stroke = (opId: string, layerId: string): CanvasOp => ({
  op_id: opId,
  type: "stroke",
  layer_id: layerId,
  tool: "brush",
  color: "#fff",
  size: 10,
  points: [[1, 1]],
});
const undo = (opId: string, target: string): CanvasOp => ({ op_id: opId, type: "undo", target });

describe("activeOps (undo-as-op)", () => {
  it("an active undo suppresses its target", () => {
    const ops = [addLayer("a"), stroke("s1", "a"), undo("u1", "s1")];
    expect(activeOps(ops).map((o) => o.op_id)).toEqual(["add_a"]);
  });

  it("undoing an undo is redo", () => {
    const ops = [addLayer("a"), stroke("s1", "a"), undo("u1", "s1"), undo("u2", "u1")];
    expect(activeOps(ops).map((o) => o.op_id)).toEqual(["add_a", "s1"]);
  });

  it("a three-deep chain resolves (undo the redo)", () => {
    const ops = [addLayer("a"), stroke("s1", "a"), undo("u1", "s1"), undo("u2", "u1"), undo("u3", "u2")];
    expect(activeOps(ops).map((o) => o.op_id)).toEqual(["add_a"]);
  });
});

describe("reduce", () => {
  it("builds layers bottom→top and applies strokes to paint layers only", () => {
    const state = reduce([
      addLayer("img", "image"),
      addLayer("paint"),
      stroke("s1", "paint"),
      stroke("s2", "img"), // image layers are immutable pixels
      stroke("s3", "missing"), // and unknown layers are ignored
    ]);
    expect(state.layers.map((l) => l.id)).toEqual(["img", "paint"]);
    expect(state.layers[1].strokes.map((s) => s.op_id)).toEqual(["s1"]);
    expect(state.layers[0].strokes).toEqual([]);
  });

  it("undoing remove_layer restores the layer WITH its strokes", () => {
    const ops: CanvasOp[] = [
      addLayer("a"),
      stroke("s1", "a"),
      { op_id: "rm", type: "remove_layer", layer_id: "a" },
    ];
    expect(reduce(ops).layers).toHaveLength(0);
    ops.push(undo("u1", "rm"));
    const state = reduce(ops);
    expect(state.layers).toHaveLength(1);
    expect(state.layers[0].strokes.map((s) => s.op_id)).toEqual(["s1"]);
  });

  it("set_layer merges props and clamps, index moves reorder", () => {
    const state = reduce([
      addLayer("a"),
      addLayer("b"),
      { op_id: "p1", type: "set_layer", layer_id: "a", props: { opacity: 7, index: 1 } },
    ]);
    expect(state.layers.map((l) => l.id)).toEqual(["b", "a"]);
    expect(state.layers[1].opacity).toBe(1);
  });

  it("duplicate add_layer is a no-op", () => {
    const state = reduce([addLayer("a"), addLayer("a")]);
    expect(state.layers).toHaveLength(1);
  });
});

describe("CanvasDoc undo/redo", () => {
  it("round-trips undo→redo→undo and clears redo on new ops", () => {
    const doc = new CanvasDoc([addLayer("a")]);
    doc.apply(stroke("s1", "a"));
    expect(doc.state.layers[0].strokes).toHaveLength(1);

    doc.undo();
    expect(doc.state.layers[0].strokes).toHaveLength(0);
    expect(doc.canRedo).toBe(true);

    doc.redo();
    expect(doc.state.layers[0].strokes).toHaveLength(1);

    doc.undo();
    doc.apply(stroke("s2", "a")); // new op clears the redo chain
    expect(doc.canRedo).toBe(false);
    expect(doc.state.layers[0].strokes.map((s) => s.op_id)).toEqual(["s2"]);
  });

  it("emits every locally-authored op (including undos) to onLocalOp", () => {
    const doc = new CanvasDoc([addLayer("a")]);
    const seen: string[] = [];
    doc.onLocalOp = (op) => seen.push(op.type);
    doc.apply(stroke("s1", "a"));
    doc.undo();
    doc.redo();
    expect(seen).toEqual(["stroke", "undo", "undo"]);
  });

  it("constructor dedups by op_id (at-least-once server log)", () => {
    const doc = new CanvasDoc([addLayer("a"), stroke("s1", "a"), stroke("s1", "a")]);
    expect(doc.ops).toHaveLength(2);
    expect(doc.state.layers[0].strokes).toHaveLength(1);
  });

  it("applyRemote dedups ops it already holds", () => {
    const doc = new CanvasDoc([addLayer("a")]);
    doc.applyRemote([addLayer("a"), stroke("s1", "a")]);
    doc.applyRemote([stroke("s1", "a")]);
    expect(doc.ops).toHaveLength(2);
    expect(doc.state.layers[0].strokes).toHaveLength(1);
  });
});
