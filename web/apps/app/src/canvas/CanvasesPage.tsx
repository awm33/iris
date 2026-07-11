import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { newOpId } from "@iris/doc-runtime";
import { canvasClient } from "../api";

export function CanvasesPage(props: { projectId: string; onOpen: (id: string) => void }) {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [confirmDelete, setConfirmDelete] = useState<string>();

  const canvases = useQuery({
    queryKey: ["canvases", props.projectId],
    queryFn: () => canvasClient.listCanvases({ projectId: props.projectId }),
  });

  const create = useMutation({
    mutationFn: async (n: string) => {
      const r = await canvasClient.createCanvas({
        projectId: props.projectId,
        name: n,
        width: 1920,
        height: 1080,
      });
      // Seed a paint layer: a blank canvas where the brush silently does
      // nothing is a dead end.
      await canvasClient.appendOps({
        canvasId: r.canvas!.id,
        baseSeq: 0n,
        payloads: [
          JSON.stringify({
            op_id: newOpId(),
            type: "add_layer",
            layer: { id: `lyr_${newOpId().slice(3)}`, name: "Layer 1", kind: "paint" },
          }),
        ],
      });
      return r;
    },
    onSuccess: (r) => {
      setName("");
      void qc.invalidateQueries({ queryKey: ["canvases"] });
      if (r.canvas) props.onOpen(r.canvas.id);
    },
  });

  const remove = useMutation({
    mutationFn: (id: string) => canvasClient.deleteCanvas({ id }),
    onSuccess: () => {
      setConfirmDelete(undefined);
      void qc.invalidateQueries({ queryKey: ["canvases"] });
    },
  });

  const submit = () => {
    const n = name.trim();
    if (n) create.mutate(n);
  };

  return (
    <div>
      <h2>Canvases</h2>
      <div className="toolbar">
        <input
          type="text"
          placeholder="New canvas name… (1920×1080)"
          value={name}
          onChange={(e) => setName(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && submit()}
        />
        <button className="btn" disabled={!name.trim() || create.isPending} onClick={submit}>
          {create.isPending ? "Creating…" : "New canvas"}
        </button>
        {create.isError && <span className="status error">{String(create.error)}</span>}
        {remove.isError && <span className="status error">{String(remove.error)}</span>}
      </div>

      {canvases.isLoading && <div className="empty">Loading…</div>}
      {canvases.data && canvases.data.canvases.length === 0 && (
        <div className="empty">
          No canvases yet — create one above, or open any Library image with 🎨 Canvas.
        </div>
      )}
      <div className="grid">
        {canvases.data?.canvases.map((c) => (
          <div key={c.id} className="card">
            {/* The open target is a real button (keyboard-reachable); the
                Delete row stays a sibling — buttons must not nest. */}
            <button className="card-button" onClick={() => props.onOpen(c.id)} title="Open canvas">
              <div className="thumb-placeholder">🎨</div>
              <div className="name">{c.name}</div>
              <div className="meta">
                {c.width}×{c.height} · {c.headSeq.toString()} ops
              </div>
            </button>
            <div className="promote-row">
              {confirmDelete === c.id ? (
                <>
                  <button
                    className="btn secondary chip-add"
                    disabled={remove.isPending}
                    onClick={() => remove.mutate(c.id)}
                  >
                    Really delete
                  </button>
                  <button className="btn secondary chip-add" onClick={() => setConfirmDelete(undefined)}>
                    Keep
                  </button>
                </>
              ) : (
                <button className="btn secondary chip-add" onClick={() => setConfirmDelete(c.id)}>
                  Delete
                </button>
              )}
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}
