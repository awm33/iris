import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import type { Canvas } from "@iris/api-client";
import {
  CanvasDoc,
  type CanvasOp,
  newOpId,
  OpSync,
  type OpSyncTransport,
  parseOp,
  type SyncStatus,
} from "@iris/doc-runtime";
import { assetClient, canvasClient, canvasKeepaliveClient, uploadFile } from "../api";
import { flatten, LayerRasterCache } from "./renderer";
import { CanvasViewport, type CanvasTool } from "./CanvasViewport";

interface Session {
  canvas: Canvas;
  doc: CanvasDoc;
  sync: OpSync;
  cache: LayerRasterCache;
}

const parseOps = (payloads: string[]): CanvasOp[] =>
  payloads.map(parseOp).filter((o): o is CanvasOp => o !== null);

export function CanvasPage(props: { canvasId: string; projectId: string; onBack: () => void }) {
  const qc = useQueryClient();
  const [session, setSession] = useState<Session | null>(null);
  const [loadError, setLoadError] = useState<string>();
  const [status, setStatus] = useState<SyncStatus>("saved");
  const [syncError, setSyncError] = useState<string>();
  const [tool, setTool] = useState<CanvasTool>("brush");
  const [color, setColor] = useState("#ff5a36");
  const [brushSize, setBrushSize] = useState(24);
  const [activeLayerId, setActiveLayerId] = useState<string>();
  const [, setDocTick] = useState(0); // re-render the layers panel on doc change
  const [redrawTick, setRedrawTick] = useState(0);
  const [preview, setPreview] = useState<{ layerId: string; opacity: number }>();
  const [exportMsg, setExportMsg] = useState<string>();
  const [exporting, setExporting] = useState(false);
  const [imageError, setImageError] = useState<string>();
  const [retrying, setRetrying] = useState(true);

  // Image-layer pixels: versionId → Image, loaded via signed URLs.
  // crossOrigin=anonymous keeps the canvas untainted so export can read it.
  const images = useRef(new Map<string, { img: HTMLImageElement; loaded: boolean }>());
  const sessionRef = useRef<Session | null>(null);
  sessionRef.current = session;
  const getImage = (versionId: string): HTMLImageElement | null => {
    let e = images.current.get(versionId);
    if (!e) {
      const img = new Image();
      img.crossOrigin = "anonymous";
      e = { img, loaded: false };
      images.current.set(versionId, e);
      const failed = () => {
        // Drop the entry so the next composite retries the load.
        images.current.delete(versionId);
        setImageError("A layer image failed to load — retrying on next redraw.");
      };
      img.onerror = failed;
      void assetClient
        .signDownload({ versionId })
        .then((r) => {
          img.onload = () => {
            e!.loaded = true;
            setImageError(undefined);
            const s = sessionRef.current;
            if (s) s.cache.invalidateImage(versionId, s.doc.state);
            setRedrawTick((t) => t + 1);
          };
          img.src = r.url;
        })
        .catch(failed);
    }
    return e.loaded ? e.img : null;
  };

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        // Page through the op log until we hold everything up to head_seq.
        let after = 0n;
        let canvas: Canvas | undefined;
        const payloads: string[] = [];
        for (;;) {
          const r = await canvasClient.getCanvas({ id: props.canvasId, afterSeq: after });
          canvas = r.canvas!;
          for (const op of r.ops) payloads.push(op.payload);
          if (r.ops.length === 0 || r.ops[r.ops.length - 1].seq >= canvas.headSeq) break;
          after = r.ops[r.ops.length - 1].seq;
        }
        if (cancelled || !canvas) return;

        const doc = new CanvasDoc(parseOps(payloads));
        const cache = new LayerRasterCache(canvas.width, canvas.height, getImage);
        const transport: OpSyncTransport = {
          append: async (baseSeq, ps, opts) => {
            // keepalive only for unload-time flushes: it survives tab close
            // but the platform caps its body at 64KB (a big batch just fails
            // and stays pending — no worse than a killed ordinary fetch).
            const client = opts?.keepalive ? canvasKeepaliveClient : canvasClient;
            const r = await client.appendOps({
              canvasId: props.canvasId,
              baseSeq: BigInt(baseSeq),
              payloads: ps,
            });
            return Number(r.headSeq);
          },
          fetchSince: async (seq) => {
            let cursor = BigInt(seq);
            const missed: string[] = [];
            let head = seq;
            for (;;) {
              const r = await canvasClient.getCanvas({ id: props.canvasId, afterSeq: cursor });
              for (const op of r.ops) missed.push(op.payload);
              head = Number(r.canvas!.headSeq);
              if (r.ops.length === 0 || r.ops[r.ops.length - 1].seq >= r.canvas!.headSeq) break;
              cursor = r.ops[r.ops.length - 1].seq;
            }
            return { headSeq: head, payloads: missed };
          },
        };
        const sync = new OpSync(transport, Number(canvas.headSeq));
        doc.onLocalOp = (op) => sync.enqueue(op);
        sync.onRemoteOps = (ps) => doc.applyRemote(parseOps(ps));
        sync.onStatus = (s, err) => {
          setStatus(s);
          setSyncError(err);
          setRetrying(sync.retrying);
        };
        setSession({ canvas, doc, sync, cache });
        const layers = doc.state.layers;
        const topPaint = [...layers].reverse().find((l) => l.kind === "paint");
        setActiveLayerId((topPaint ?? layers[layers.length - 1])?.id);
      } catch (e) {
        if (!cancelled) setLoadError(String(e));
      }
    })();
    return () => {
      cancelled = true;
    };
    // getImage is stable-enough (ref-backed); the session is rebuilt per canvas id.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [props.canvasId]);

  // Layers panel re-renders on every doc change; pending ops flush on leave.
  useEffect(() => {
    if (!session) return;
    const unsub = session.doc.subscribe(() => setDocTick((t) => t + 1));
    // urgent=true → keepalive fetch: an ordinary fetch is killed on unload
    // and the debounced batch would be silently lost.
    const flush = () => void session.sync.flush(true);
    window.addEventListener("beforeunload", flush);
    document.addEventListener("visibilitychange", flush);
    return () => {
      unsub();
      window.removeEventListener("beforeunload", flush);
      document.removeEventListener("visibilitychange", flush);
      flush();
    };
  }, [session]);

  // Cmd/Ctrl+Z undo, +Shift redo — never while typing in a field.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement;
      if (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.isContentEditable) return;
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "z") {
        e.preventDefault();
        if (e.shiftKey) session?.doc.redo();
        else session?.doc.undo();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [session]);

  if (loadError) return <div className="status error">Couldn’t open canvas: {loadError}</div>;
  if (!session) return <div className="empty">Opening canvas…</div>;

  const { doc, canvas, cache } = session;
  const layers = doc.state.layers;
  const active = layers.find((l) => l.id === activeLayerId);

  const commitOpacityPreview = () => {
    if (!preview) return;
    doc.apply({
      op_id: newOpId(),
      type: "set_layer",
      layer_id: preview.layerId,
      props: { opacity: preview.opacity },
    });
    setPreview(undefined);
  };

  const addLayer = () => {
    const id = `lyr_${newOpId().slice(3)}`;
    doc.apply({
      op_id: newOpId(),
      type: "add_layer",
      layer: { id, name: `Layer ${layers.length + 1}`, kind: "paint" },
    });
    setActiveLayerId(id);
  };

  const doExport = async () => {
    // An image layer still loading composites as a blank raster — exporting
    // now would silently upload a PNG missing the base image.
    const stillLoading = doc.state.layers.some(
      (l) => l.kind === "image" && l.visible && l.versionId && !images.current.get(l.versionId)?.loaded,
    );
    if (stillLoading) {
      setExportMsg("Layer images are still loading — try again in a moment.");
      return;
    }
    setExporting(true);
    setExportMsg(undefined);
    try {
      await session.sync.flush();
      const flat = flatten(doc.state, cache, canvas.width, canvas.height);
      const blob = await new Promise<Blob | null>((res) => flat.toBlob(res, "image/png"));
      if (!blob) throw new Error("could not encode PNG");
      await uploadFile(new File([blob], `${canvas.name}.png`, { type: "image/png" }), props.projectId);
      void qc.invalidateQueries({ queryKey: ["assets"] });
      setExportMsg("Exported to Library ✓");
    } catch (e) {
      setExportMsg(`Export failed: ${String(e)}`);
    } finally {
      setExporting(false);
    }
  };

  const toolButton = (t: CanvasTool, label: string) => (
    <button className={`btn secondary${tool === t ? " tool-active" : ""}`} onClick={() => setTool(t)}>
      {label}
    </button>
  );

  return (
    <div className="canvas-page">
      <div className="toolbar canvas-toolbar">
        <button className="btn secondary" onClick={props.onBack}>
          ←
        </button>
        <span className="truncate" style={{ maxWidth: 220 }}>
          {canvas.name}
        </span>
        <span className="meta">
          {canvas.width}×{canvas.height}
        </span>
        {toolButton("pan", "✋ Pan")}
        {toolButton("brush", "🖌 Brush")}
        {toolButton("eraser", "◻ Eraser")}
        <input
          type="color"
          value={color}
          onChange={(e) => setColor(e.target.value)}
          title="Brush color"
          aria-label="Brush color"
        />
        <label className="meta">
          size
          <input
            type="range"
            min={1}
            max={200}
            value={brushSize}
            onChange={(e) => setBrushSize(Number(e.target.value))}
          />
        </label>
        <button className="btn secondary" disabled={!doc.canUndo} onClick={() => doc.undo()} title="Cmd+Z">
          ↩
        </button>
        <button
          className="btn secondary"
          disabled={!doc.canRedo}
          onClick={() => doc.redo()}
          title="Shift+Cmd+Z"
        >
          ↪
        </button>
        <span className={`status${status === "error" ? " error" : ""}`}>
          {status === "saved" && "saved"}
          {status === "pending" && "…"}
          {status === "saving" && "saving…"}
          {status === "error" &&
            `save failed: ${syncError ?? ""} ${retrying ? "(retrying)" : "(ops kept locally — not retryable)"}`}
        </span>
        {imageError && <span className="status error">{imageError}</span>}
        <span style={{ flex: 1 }} />
        {exportMsg && <span className="status">{exportMsg}</span>}
        <button className="btn" disabled={exporting} onClick={() => void doExport()}>
          {exporting ? "Exporting…" : "Export to Library"}
        </button>
      </div>

      <div className="canvas-body">
        <CanvasViewport
          doc={doc}
          cache={cache}
          docW={canvas.width}
          docH={canvas.height}
          tool={tool}
          color={color}
          brushSize={brushSize}
          activeLayerId={activeLayerId}
          redrawTick={redrawTick}
          preview={preview}
        />
        <aside className="layers-panel">
          <div className="section-head">
            <h3>Layers</h3>
            <button className="btn secondary chip-add" onClick={addLayer}>
              + Layer
            </button>
          </div>
          {[...layers].reverse().map((l) => (
            <div
              key={l.id}
              className={`layer-row${l.id === activeLayerId ? " selected" : ""}`}
              onClick={() => setActiveLayerId(l.id)}
            >
              <button
                className="chip-x"
                title={l.visible ? "Hide" : "Show"}
                onClick={(e) => {
                  e.stopPropagation();
                  doc.apply({
                    op_id: newOpId(),
                    type: "set_layer",
                    layer_id: l.id,
                    props: { visible: !l.visible },
                  });
                }}
              >
                {l.visible ? "👁" : "◡"}
              </button>
              <span className="truncate" style={{ flex: 1 }}>
                {l.name}
                {l.kind === "image" && <span className="meta"> · image</span>}
              </span>
              <button
                className="chip-x"
                title="Delete layer (undoable)"
                onClick={(e) => {
                  e.stopPropagation();
                  doc.apply({ op_id: newOpId(), type: "remove_layer", layer_id: l.id });
                  if (activeLayerId === l.id) setActiveLayerId(undefined);
                }}
              >
                ×
              </button>
            </div>
          ))}
          {active && (
            <label className="meta layer-opacity">
              opacity {Math.round((preview?.layerId === active.id ? preview.opacity : active.opacity) * 100)}%
              <input
                type="range"
                min={0}
                max={100}
                value={Math.round((preview?.layerId === active.id ? preview.opacity : active.opacity) * 100)}
                onChange={(e) => setPreview({ layerId: active.id, opacity: Number(e.target.value) / 100 })}
                // Commit on every way the gesture can end — a missed
                // pointerup would leave the preview diverged from the doc.
                onPointerUp={commitOpacityPreview}
                onPointerCancel={commitOpacityPreview}
                onLostPointerCapture={commitOpacityPreview}
                onBlur={commitOpacityPreview}
              />
            </label>
          )}
          {active?.kind === "image" && (
            <div className="meta">Image layers are pixels from the Library — paint on a paint layer above.</div>
          )}
        </aside>
      </div>
    </div>
  );
}
