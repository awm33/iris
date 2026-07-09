import { type CanvasOp, type LayerKind, type StrokeOp, newOpId } from "./ops";

export interface LayerState {
  id: string;
  name: string;
  kind: LayerKind;
  versionId?: string;
  opacity: number;
  visible: boolean;
  /** Active strokes in application order — the renderer rasterizes these. */
  strokes: StrokeOp[];
}

export interface CanvasDocState {
  /** Bottom → top. */
  layers: LayerState[];
}

/**
 * Resolve undo-as-op semantics: an op is active unless targeted by an active
 * undo. Walking the log in reverse makes this one pass — an undo that is
 * itself suppressed (i.e. redone) never suppresses its target, so chains of
 * undo→redo→undo resolve correctly.
 */
export function activeOps(ops: CanvasOp[]): CanvasOp[] {
  const suppressed = new Set<string>();
  for (let i = ops.length - 1; i >= 0; i--) {
    const op = ops[i];
    if (op.type === "undo" && !suppressed.has(op.op_id)) suppressed.add(op.target);
  }
  return ops.filter((op) => op.type !== "undo" && !suppressed.has(op.op_id));
}

/** Pure replay: full log → document state. */
export function reduce(ops: CanvasOp[]): CanvasDocState {
  const layers: LayerState[] = [];
  const at = (id: string) => layers.findIndex((l) => l.id === id);
  for (const op of activeOps(ops)) {
    switch (op.type) {
      case "add_layer": {
        if (at(op.layer.id) !== -1) break; // duplicate add is a no-op
        const layer: LayerState = {
          id: op.layer.id,
          name: op.layer.name,
          kind: op.layer.kind,
          versionId: op.layer.version_id,
          opacity: op.layer.opacity ?? 1,
          visible: op.layer.visible ?? true,
          strokes: [],
        };
        const i = op.index === undefined ? layers.length : Math.max(0, Math.min(op.index, layers.length));
        layers.splice(i, 0, layer);
        break;
      }
      case "remove_layer": {
        const i = at(op.layer_id);
        if (i !== -1) layers.splice(i, 1);
        break;
      }
      case "set_layer": {
        const i = at(op.layer_id);
        if (i === -1) break;
        const l = layers[i];
        if (op.props.name !== undefined) l.name = op.props.name;
        if (op.props.opacity !== undefined) l.opacity = Math.max(0, Math.min(1, op.props.opacity));
        if (op.props.visible !== undefined) l.visible = op.props.visible;
        if (op.props.index !== undefined) {
          const j = Math.max(0, Math.min(op.props.index, layers.length - 1));
          layers.splice(i, 1);
          layers.splice(j, 0, l);
        }
        break;
      }
      case "stroke": {
        const i = at(op.layer_id);
        // Strokes only land on paint layers; image layers are immutable pixels.
        if (i !== -1 && layers[i].kind === "paint") layers[i].strokes.push(op);
        break;
      }
    }
  }
  return { layers };
}

/**
 * The document: full op log + derived state + undo/redo + local-op fanout.
 * Pure model — persistence is the OpSync's job (subscribe via onLocalOp).
 */
export class CanvasDoc {
  ops: CanvasOp[];
  state: CanvasDocState;
  /** Called for every locally-authored op (the sync layer enqueues these). */
  onLocalOp?: (op: CanvasOp) => void;

  private listeners = new Set<() => void>();
  /** Undo ops we authored whose undo-ing is the next redo, newest last. */
  private redoTargets: string[] = [];

  constructor(ops: CanvasOp[] = []) {
    this.ops = ops;
    this.state = reduce(this.ops);
  }

  subscribe(fn: () => void): () => void {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  private commit(op: CanvasOp) {
    this.ops.push(op);
    this.state = reduce(this.ops);
    this.onLocalOp?.(op);
    this.listeners.forEach((fn) => fn());
  }

  /** Author a new op (clears the redo chain). */
  apply(op: CanvasOp) {
    this.redoTargets = [];
    this.commit(op);
  }

  /** Ops arriving from the server (another tab/actor): append + replay. */
  applyRemote(ops: CanvasOp[]) {
    if (ops.length === 0) return;
    const known = new Set(this.ops.map((o) => o.op_id));
    const fresh = ops.filter((o) => !known.has(o.op_id));
    if (fresh.length === 0) return;
    this.ops.push(...fresh);
    this.state = reduce(this.ops);
    this.listeners.forEach((fn) => fn());
  }

  get canUndo(): boolean {
    return activeOps(this.ops).length > 0;
  }
  get canRedo(): boolean {
    return this.redoTargets.length > 0;
  }

  undo() {
    const active = activeOps(this.ops);
    const last = active[active.length - 1];
    if (!last) return;
    const op: CanvasOp = { op_id: newOpId(), type: "undo", target: last.op_id };
    this.redoTargets.push(op.op_id);
    this.commit(op);
  }

  redo() {
    const target = this.redoTargets.pop();
    if (!target) return;
    this.commit({ op_id: newOpId(), type: "undo", target });
  }
}
