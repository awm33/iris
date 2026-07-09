import { useEffect, useRef } from "react";
import { type CanvasDoc, newOpId, type StrokeOp } from "@iris/doc-runtime";
import { composite, drawLiveSegment, drawStroke, type LayerRasterCache, type ViewTransform } from "./renderer";

export type CanvasTool = "pan" | "brush" | "eraser";

/**
 * The drawing surface: composites the doc under a pan/zoom transform and
 * turns pointer input into stroke ops. Live strokes draw straight into the
 * layer raster (segment-at-a-time — identical pixels to the committed
 * replay), then commit as one op on release.
 */
export function CanvasViewport(props: {
  doc: CanvasDoc;
  cache: LayerRasterCache;
  docW: number;
  docH: number;
  tool: CanvasTool;
  color: string;
  brushSize: number;
  activeLayerId?: string;
  /** Bumped by the page when an image layer's pixels arrive or a preview
   * (e.g. opacity drag) changes — forces a recomposite. */
  redrawTick: number;
  /** Uncommitted layer-prop preview (opacity slider mid-drag). */
  preview?: { layerId: string; opacity: number };
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const vtRef = useRef<ViewTransform>({ x: 0, y: 0, scale: 1 });
  const fittedRef = useRef(false);
  const drawPending = useRef(false);
  const propsRef = useRef(props);
  propsRef.current = props;

  const stroke = useRef<{ points: [number, number][]; layerId: string } | null>(null);
  const panning = useRef<{ startX: number; startY: number; vtX: number; vtY: number } | null>(null);

  const requestDraw = () => {
    if (drawPending.current) return;
    drawPending.current = true;
    requestAnimationFrame(() => {
      drawPending.current = false;
      const canvas = canvasRef.current;
      if (!canvas) return;
      const p = propsRef.current;
      const dpr = window.devicePixelRatio || 1;
      // Layer-prop preview: composite with the overridden opacity without
      // touching the doc (the op commits on slider release).
      const state = p.preview
        ? {
            layers: p.doc.state.layers.map((l) =>
              l.id === p.preview!.layerId ? { ...l, opacity: p.preview!.opacity } : l,
            ),
          }
        : p.doc.state;
      composite(canvas, state, p.cache, vtRef.current, p.docW, p.docH, dpr);
    });
  };
  const requestDrawRef = useRef(requestDraw);
  requestDrawRef.current = requestDraw;

  // Resize backing store with the container; fit the doc on first layout.
  useEffect(() => {
    const container = containerRef.current!;
    const canvas = canvasRef.current!;
    const ro = new ResizeObserver(() => {
      const rect = container.getBoundingClientRect();
      const dpr = window.devicePixelRatio || 1;
      canvas.width = Math.max(1, Math.round(rect.width * dpr));
      canvas.height = Math.max(1, Math.round(rect.height * dpr));
      canvas.style.width = `${rect.width}px`;
      canvas.style.height = `${rect.height}px`;
      if (!fittedRef.current && rect.width > 0) {
        fittedRef.current = true;
        const p = propsRef.current;
        const scale = Math.min(rect.width / p.docW, rect.height / p.docH) * 0.9 || 1;
        vtRef.current = {
          scale,
          x: (rect.width - p.docW * scale) / 2,
          y: (rect.height - p.docH * scale) / 2,
        };
      }
      requestDrawRef.current();
    });
    ro.observe(container);
    return () => ro.disconnect();
  }, []);

  // Redraw on doc changes and page-driven ticks.
  useEffect(() => props.doc.subscribe(() => requestDrawRef.current()), [props.doc]);
  useEffect(() => {
    requestDrawRef.current();
  }, [props.redrawTick, props.preview]);

  // Wheel zoom must preventDefault → non-passive listener.
  useEffect(() => {
    const canvas = canvasRef.current!;
    const onWheel = (e: WheelEvent) => {
      e.preventDefault();
      const rect = canvas.getBoundingClientRect();
      const cx = e.clientX - rect.left;
      const cy = e.clientY - rect.top;
      const vt = vtRef.current;
      const next = Math.min(16, Math.max(0.05, vt.scale * Math.exp(-e.deltaY * 0.0015)));
      const k = next / vt.scale;
      vtRef.current = { scale: next, x: cx - (cx - vt.x) * k, y: cy - (cy - vt.y) * k };
      requestDrawRef.current();
    };
    canvas.addEventListener("wheel", onWheel, { passive: false });
    return () => canvas.removeEventListener("wheel", onWheel);
  }, []);

  const toDoc = (e: React.PointerEvent): [number, number] => {
    const rect = canvasRef.current!.getBoundingClientRect();
    const vt = vtRef.current;
    return [(e.clientX - rect.left - vt.x) / vt.scale, (e.clientY - rect.top - vt.y) / vt.scale];
  };

  const liveStrokeStyle = (): Pick<StrokeOp, "tool" | "color" | "size"> => ({
    tool: props.tool === "eraser" ? "eraser" : "brush",
    color: props.color,
    size: props.brushSize,
  });

  // Capture keeps strokes/pans alive when the pointer leaves the viewport;
  // it can throw for already-released pointers — losing capture only means
  // the stroke ends at the edge, so never let it break the gesture.
  const capture = (e: React.PointerEvent) => {
    try {
      e.currentTarget.setPointerCapture(e.pointerId);
    } catch {
      /* gesture continues uncaptured */
    }
  };

  const onPointerDown = (e: React.PointerEvent) => {
    if (e.button === 1 || props.tool === "pan") {
      panning.current = { startX: e.clientX, startY: e.clientY, vtX: vtRef.current.x, vtY: vtRef.current.y };
      capture(e);
      return;
    }
    if (e.button !== 0) return;
    const p = propsRef.current;
    const layer = p.doc.state.layers.find((l) => l.id === p.activeLayerId);
    if (!layer || layer.kind !== "paint" || !layer.visible) return;
    const pt = toDoc(e);
    stroke.current = { points: [pt], layerId: layer.id };
    capture(e);
    const ctx = p.cache.liveContext(layer.id);
    // A press with no movement is a dot — same shape the replay draws.
    if (ctx) drawStroke(ctx, { op_id: "", type: "stroke", layer_id: layer.id, points: [pt], ...liveStrokeStyle() });
    requestDraw();
  };

  const onPointerMove = (e: React.PointerEvent) => {
    if (panning.current) {
      const pan = panning.current;
      vtRef.current = {
        ...vtRef.current,
        x: pan.vtX + (e.clientX - pan.startX),
        y: pan.vtY + (e.clientY - pan.startY),
      };
      requestDraw();
      return;
    }
    const s = stroke.current;
    if (!s) return;
    const pt = toDoc(e);
    const last = s.points[s.points.length - 1];
    if (Math.hypot(pt[0] - last[0], pt[1] - last[1]) * vtRef.current.scale < 1.5) return; // decimate
    const ctx = propsRef.current.cache.liveContext(s.layerId);
    if (ctx) drawLiveSegment(ctx, liveStrokeStyle(), last, pt);
    s.points.push(pt);
    requestDraw();
  };

  const onPointerUp = () => {
    panning.current = null;
    const s = stroke.current;
    if (!s) return;
    stroke.current = null;
    const p = propsRef.current;
    const op: StrokeOp = {
      op_id: newOpId(),
      type: "stroke",
      layer_id: s.layerId,
      points: s.points.map(([x, y]) => [Math.round(x * 10) / 10, Math.round(y * 10) / 10]),
      ...liveStrokeStyle(),
    };
    // The live segments already rasterized these exact pixels — record the
    // op as drawn BEFORE the doc replays it, or it would draw twice (visible
    // as darkened anti-aliased edges).
    p.cache.noteLiveStrokeCommitted(s.layerId, op.op_id);
    p.doc.apply(op);
  };

  const cursor = props.tool === "pan" ? "grab" : "crosshair";
  return (
    <div ref={containerRef} className="canvas-viewport">
      <canvas
        ref={canvasRef}
        style={{ cursor, touchAction: "none" }}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onPointerCancel={onPointerUp}
      />
    </div>
  );
}
