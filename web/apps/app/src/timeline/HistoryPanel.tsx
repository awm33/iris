import { activeOps, type CanvasOp, type TimelineDoc, type TimelineOp } from "@iris/doc-runtime";

// Visual history over the op-log (the log IS the history — no extra
// bookkeeping). Newest first. Activity comes from the reducer's own
// activeOps — a hand-rolled "every undo target is dead" set diverges the
// moment someone undoes-then-redoes (redo is an undo OF an undo here).
// "↩ undo after" runs doc.undoTo: repeated undo() steps, so the revert is
// reducer-consistent, autosaves like any edit, and ⇧⌘Z walks it back.
// Timeline-first; the canvas gets the same panel when the doc shell
// generalizes (the recorded M4 ask).

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
    case "set_clip_text":
      return `Edit caption “${op.text.length > 24 ? op.text.slice(0, 24) + "…" : op.text}”`;
    case "undo":
      return "Undo";
  }
}

export function HistoryPanel(props: { doc: TimelineDoc; onClose: () => void }) {
  const ops = props.doc.ops;
  const activeIds = new Set(
    (activeOps(ops as unknown as CanvasOp[]) as unknown as TimelineOp[]).map((o) => o.op_id),
  );

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
          const inactive = op.type !== "undo" && !activeIds.has(op.op_id);
          return (
            <div key={op.op_id} className={`history-row${inactive ? " inactive" : ""}${op.type === "undo" ? " is-undo" : ""}`}>
              <span className="truncate">{label(op)}</span>
              {!inactive && op.type !== "undo" && i < ops.length - 1 && (
                <button
                  className="chip-x"
                  title="Undo everything after this point (⇧⌘Z steps back through the revert)"
                  onClick={() => props.doc.undoTo(i)}
                >
                  ↩ undo after
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
