// Canvas op vocabulary v1 (client-owned; the server validates only the
// {op_id, type} envelope — see proto/iris/v1/canvas.proto). Designed to
// generalize: the envelope + undo-as-op semantics are shared with the
// timeline doc (M5); only the op types differ.

export type LayerKind = "image" | "paint";

export interface LayerInit {
  id: string;
  name: string;
  kind: LayerKind;
  /** Image layers: the content-addressed asset version rendered as pixels. */
  version_id?: string;
  opacity?: number; // 0..1, default 1
  visible?: boolean; // default true
}

export interface StrokeOp {
  op_id: string;
  type: "stroke";
  layer_id: string;
  tool: "brush" | "eraser";
  color: string; // css hex; ignored by eraser
  size: number; // diameter in doc pixels
  points: [number, number][]; // doc coordinates
}

export type CanvasOp =
  | { op_id: string; type: "add_layer"; layer: LayerInit; index?: number }
  | { op_id: string; type: "remove_layer"; layer_id: string }
  | {
      op_id: string;
      type: "set_layer";
      layer_id: string;
      props: { name?: string; opacity?: number; visible?: boolean; index?: number };
    }
  | StrokeOp
  /**
   * Undo-as-op: history stays append-only and survives reload/refetch.
   * An op is inactive when targeted by an active undo; undoing an undo is
   * redo. See activeOps().
   */
  | { op_id: string; type: "undo"; target: string };

/** Monotonic-ish unique op ids (single actor per tab is the v1 reality).
 * Counter starts at a random offset so two tabs opened in the same
 * millisecond don't walk the same sequence. */
let counter = Math.floor(Math.random() * 46656);
export function newOpId(): string {
  counter = (counter + 1) % 46656; // base36 ^3
  return `op_${Date.now().toString(36)}${counter.toString(36).padStart(3, "0")}${Math.floor(
    Math.random() * 46656,
  )
    .toString(36)
    .padStart(3, "0")}`;
}

/** Parse a persisted payload; returns null for ops this runtime can't read
 * (newer vocabulary) — replay skips them instead of crashing. */
export function parseOp(payload: string): CanvasOp | null {
  try {
    const op = JSON.parse(payload) as CanvasOp;
    if (typeof op !== "object" || op === null || !op.op_id || !op.type) return null;
    return op;
  } catch {
    return null;
  }
}
