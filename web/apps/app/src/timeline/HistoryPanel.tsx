import { newOpId, type TimelineDoc, type TimelineOp } from "@iris/doc-runtime";

// Visual history over the op-log (the log IS the history — no extra
// bookkeeping). Newest first; entries deactivated by an undo render
// struck-through. "↩ to here" issues undo ops for every ACTIVE op after
// the target — reusing undo-as-op semantics, so the jump itself is
// undoable and syncs like any other edit. Timeline-first; the canvas gets
// the same panel when the doc shell generalizes (the recorded M4 ask).

function label(op: TimelineOp): string {
  switch (op.type) {
    case "add_track":
      return `Add track ${op.track.name ?? op.track.kind}`;
    case "remove_track":
      return "Remove track";
    case "add_clip":
      return `Add clip ${op.clip.name}`;
    case "remove_clip":
      return "Remove clip";
    case "move_clip":
      return `Move clip → ${op.start.toFixed(2)}s`;
    case "trim_clip":
      return "Trim clip";
    case "undo":
      return "Undo";
  }
}

export function HistoryPanel(props: { doc: TimelineDoc; onClose: () => void }) {
  const ops = props.doc.ops;
  // Active = not deactivated by a later undo (same rule as the reducer).
  const undone = new Set(ops.filter((o) => o.type === "undo").map((o) => (o as { target: string }).target));

  const revertTo = (index: number) => {
    // Undo every active non-undo op AFTER index, newest first — each undo
    // is a normal op: autosaved, rebased, and itself undoable.
    for (let i = ops.length - 1; i > index; i--) {
      const op = ops[i];
      if (op.type === "undo" || undone.has(op.op_id)) continue;
      props.doc.apply({ op_id: newOpId(), type: "undo", target: op.op_id });
    }
  };

  return (
    <div className="history-panel">
      <div className="panel-header">
        <h3>History</h3>
        <span className="meta">{ops.length} ops</span>
        <button className="btn secondary" onClick={props.onClose}>×</button>
      </div>
      <div className="history-list">
        {[...ops].reverse().map((op, ri) => {
          const i = ops.length - 1 - ri;
          const inactive = undone.has(op.op_id);
          return (
            <div key={op.op_id} className={`history-row${inactive ? " inactive" : ""}${op.type === "undo" ? " is-undo" : ""}`}>
              <span className="truncate">{label(op)}</span>
              {!inactive && op.type !== "undo" && i < ops.length - 1 && (
                <button className="chip-x" title="Undo everything after this point" onClick={() => revertTo(i)}>
                  ↩ to here
                </button>
              )}
            </div>
          );
        })}
        {ops.length === 0 && <div className="empty">No edits yet.</div>}
      </div>
    </div>
  );
}
