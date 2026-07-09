import { useEffect, useRef } from "react";
import { type CanvasDoc, type LayerState, newOpId, type StrokeOp } from "@iris/doc-runtime";
import { composite, drawLiveSegment, drawStroke, type LayerRasterCache, type ViewTransform } from "./renderer";
import { type Selection, traceSelection } from "./genfill";

export type CanvasTool = "pan" | "brush" | "eraser" | "marquee" | "lasso" | "subject";

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
  /** Active selection (doc coordinates) — drawn as marching ants. */
  selection?: Selection;
  onSelectionChange?: (sel: Selection | undefined) => void;
  /** Ephemeral top layer (gen-fill candidate being previewed in place). */
  overlayLayer?: LayerState;
  /** Subject tool: a click in doc coordinates (shiftKey = exclude region). */
  onSubjectClick?: (pt: [number, number], negative: boolean) => void;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const vtRef = useRef<ViewTransform>({ x: 0, y: 0, scale: 1 });
  const fittedRef = useRef(false);
  const drawPending = useRef(false);
  const propsRef = useRef(props);
  propsRef.current = props;

  // Style is snapshotted at gesture start: a mid-drag tool/color/size change
  // (second pointer on the controls) must not misattribute the committed op.
  const stroke = useRef<{
    points: [number, number][];
    layerId: string;
    tool: "brush" | "eraser";
    color: string;
    size: number;
  } | null>(null);
  const panning = useRef<{ startX: number; startY: number; vtX: number; vtY: number } | null>(null);
  // Gesture-owned selection points: accumulating through props.selection
  // would read back a render-stale array and drop points between frames
  // (state read-modify-write across the async render boundary).
  const selecting = useRef<
    { kind: "rect"; anchor: [number, number] } | { kind: "lasso"; points: [number, number][] } | null
  >(null);

  // Strokes split at this many points: a single unbounded scribble would
  // blow the server's 64KB per-op cap and poison the save queue.
  const MAX_STROKE_POINTS = 2000;

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
      let layers = p.preview
        ? p.doc.state.layers.map((l) =>
            l.id === p.preview!.layerId ? { ...l, opacity: p.preview!.opacity } : l,
          )
        : p.doc.state.layers;
      // Gen-fill candidate preview: an ephemeral masked layer on top.
      if (p.overlayLayer) layers = [...layers, p.overlayLayer];
      composite(canvas, { layers }, p.cache, vtRef.current, p.docW, p.docH, dpr);
      drawSelectionOverlay(canvas, dpr);
    });
  };

  /** Marching-ants outline over the composite (two-pass dash for contrast);
   * bitmap selections render as a tint + dashed bbox (no path to dash). */
  const drawSelectionOverlay = (canvas: HTMLCanvasElement, dpr: number) => {
    const sel = propsRef.current.selection;
    if (!sel) return;
    const vt = vtRef.current;
    const ctx = canvas.getContext("2d")!;
    ctx.save();
    ctx.setTransform(vt.scale * dpr, 0, 0, vt.scale * dpr, vt.x * dpr, vt.y * dpr);
    if (sel.kind === "bitmap") {
      ctx.drawImage(sel.tintCanvas, 0, 0);
      ctx.beginPath();
      ctx.rect(sel.bbox.x, sel.bbox.y, sel.bbox.w, sel.bbox.h);
      ctx.lineWidth = 1.5 / vt.scale;
      ctx.strokeStyle = "rgba(255,255,255,0.7)";
      ctx.setLineDash([6 / vt.scale, 6 / vt.scale]);
      ctx.stroke();
      ctx.restore();
      return;
    }
    traceSelection(ctx, sel);
    ctx.lineWidth = 1.5 / vt.scale;
    ctx.strokeStyle = "rgba(0,0,0,0.85)";
    ctx.setLineDash([6 / vt.scale, 6 / vt.scale]);
    ctx.stroke();
    ctx.strokeStyle = "rgba(255,255,255,0.95)";
    ctx.lineDashOffset = 6 / vt.scale;
    ctx.stroke();
    ctx.restore();
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
  }, [props.redrawTick, props.preview, props.selection, props.overlayLayer]);

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
    if (props.tool === "subject") {
      propsRef.current.onSubjectClick?.(toDoc(e), e.shiftKey);
      return;
    }
    if (props.tool === "marquee" || props.tool === "lasso") {
      const pt = toDoc(e);
      capture(e);
      if (props.tool === "marquee") {
        selecting.current = { kind: "rect", anchor: pt };
        propsRef.current.onSelectionChange?.({ kind: "rect", x: pt[0], y: pt[1], w: 0, h: 0 });
      } else {
        selecting.current = { kind: "lasso", points: [pt] };
        propsRef.current.onSelectionChange?.({ kind: "lasso", points: [pt] });
      }
      return;
    }
    const p = propsRef.current;
    const layer = p.doc.state.layers.find((l) => l.id === p.activeLayerId);
    if (!layer || layer.kind !== "paint" || !layer.visible) return;
    const pt = toDoc(e);
    stroke.current = {
      points: [pt],
      layerId: layer.id,
      tool: p.tool === "eraser" ? "eraser" : "brush",
      color: p.color,
      size: p.brushSize,
    };
    capture(e);
    // If anything rebuilds the layer raster mid-gesture (keyboard undo, an
    // image layer's pixels arriving), the rebuild re-draws the partial
    // stroke from this hook — otherwise its pixels silently vanish and the
    // committed op would disagree with the raster until reload.
    p.cache.setLiveStroke(layer.id, () => {
      const s = stroke.current;
      if (!s) return null;
      return {
        op_id: "",
        type: "stroke",
        layer_id: s.layerId,
        tool: s.tool,
        color: s.color,
        size: s.size,
        points: s.points,
      };
    });
    const ctx = p.cache.liveContext(layer.id);
    // A press with no movement is a dot — same shape the replay draws.
    if (ctx)
      drawStroke(ctx, {
        op_id: "",
        type: "stroke",
        layer_id: layer.id,
        points: [pt],
        tool: stroke.current.tool,
        color: stroke.current.color,
        size: stroke.current.size,
      });
    requestDraw();
  };

  /** Commit the in-progress stroke as an op; with `continueAt`, immediately
   * begin a follow-on stroke (the point-cap split). */
  const finishStroke = (continueAt?: [number, number]) => {
    const s = stroke.current;
    if (!s) return;
    stroke.current = null;
    const p = propsRef.current;
    const op: StrokeOp = {
      op_id: newOpId(),
      type: "stroke",
      layer_id: s.layerId,
      points: s.points.map(([x, y]) => [Math.round(x * 10) / 10, Math.round(y * 10) / 10]),
      tool: s.tool,
      color: s.color,
      size: s.size,
    };
    // The live segments already rasterized these exact pixels — record the
    // op as drawn BEFORE the doc replays it, or it would draw twice (visible
    // as darkened anti-aliased edges).
    p.cache.noteLiveStrokeCommitted(s.layerId, op.op_id);
    if (continueAt) {
      stroke.current = { points: [continueAt], layerId: s.layerId, tool: s.tool, color: s.color, size: s.size };
    } else {
      p.cache.clearLiveStroke();
    }
    p.doc.apply(op);
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
    if (selecting.current) {
      const p = propsRef.current;
      const pt = toDoc(e);
      if (selecting.current.kind === "rect") {
        const [ax, ay] = selecting.current.anchor;
        p.onSelectionChange?.({
          kind: "rect",
          x: Math.min(ax, pt[0]),
          y: Math.min(ay, pt[1]),
          w: Math.abs(pt[0] - ax),
          h: Math.abs(pt[1] - ay),
        });
      } else {
        const pts = selecting.current.points;
        const last = pts[pts.length - 1];
        if (Math.hypot(pt[0] - last[0], pt[1] - last[1]) >= 2) {
          pts.push(pt);
          p.onSelectionChange?.({ kind: "lasso", points: [...pts] });
        }
      }
      requestDraw();
      return;
    }
    const s = stroke.current;
    if (!s) return;
    if (e.buttons === 0) {
      // The release happened where we couldn't see it (capture failed, tab
      // switch): finalize instead of leaving orphaned raster pixels.
      finishStroke();
      return;
    }
    const pt = toDoc(e);
    const last = s.points[s.points.length - 1];
    // Decimate in screen space but cap in doc space: zoomed-out strokes
    // must not bake angularity into the op (ops are the source of truth).
    const docDist = Math.hypot(pt[0] - last[0], pt[1] - last[1]);
    if (docDist < Math.min(1.5 / vtRef.current.scale, 2)) return;
    const ctx = propsRef.current.cache.liveContext(s.layerId);
    if (ctx) drawLiveSegment(ctx, s, last, pt);
    s.points.push(pt);
    if (s.points.length >= MAX_STROKE_POINTS) finishStroke(pt);
    requestDraw();
  };

  const onPointerUp = () => {
    panning.current = null;
    if (selecting.current) {
      selecting.current = null;
      const p = propsRef.current;
      const sel = p.selection;
      // Degenerate selections (a stray click) clear instead of lingering as
      // an invisible 0-area target.
      if (sel?.kind === "rect" && (sel.w < 3 || sel.h < 3)) p.onSelectionChange?.(undefined);
      if (sel?.kind === "lasso" && sel.points.length < 3) p.onSelectionChange?.(undefined);
      requestDraw();
      return;
    }
    finishStroke();
  };

  const cursor = props.tool === "pan" ? "grab" : "crosshair"; // marquee/lasso/brush all aim
  return (
    <div ref={containerRef} className="canvas-viewport">
      <canvas
        ref={canvasRef}
        style={{ cursor, touchAction: "none" }}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onPointerCancel={onPointerUp}
        onLostPointerCapture={onPointerUp}
      />
    </div>
  );
}
