// Canvas2D compositor for the image studio (M4 slice 1). Deliberately not
// WebGL2 yet: normal blend + opacity needs nothing the 2D API lacks, and the
// compositor interface is the seam where WebGL2 slots in when blend modes /
// adjustment layers arrive (plan rule 4: profiling, not ideology).
import type { CanvasDocState, LayerState, StrokeOp } from "@iris/doc-runtime";

export interface ViewTransform {
  x: number; // screen-px translation of doc origin
  y: number;
  scale: number;
}

interface LayerEntry {
  canvas: HTMLCanvasElement;
  ctx: CanvasRenderingContext2D;
  /** op_ids rasterized into the canvas, in order. Replay correctness: if the
   * doc's stroke list is not an extension of this, re-rasterize from scratch
   * (an undo can remove a stroke from the middle of history). */
  rasterized: string[];
  imageDrawn: boolean;
  /** The canvas was wiped this update — live (uncommitted) stroke pixels
   * were lost with it and must be redrawn from the live-stroke hook. */
  cleared: boolean;
}

export class LayerRasterCache {
  private entries = new Map<string, LayerEntry>();
  /** Luminance→alpha conversions of mask images, keyed by version id —
   * masks arrive as opaque black/white PNGs (spec: white = generate), but
   * destination-in compositing needs the mask in the alpha channel. */
  private alphaMasks = new Map<string, HTMLCanvasElement>();
  /** The in-progress stroke, if any: a mid-gesture rebuild (undo via
   * keyboard, image arriving) wipes its already-drawn pixels — the hook lets
   * the rebuild re-rasterize the partial stroke so nothing visibly vanishes
   * under the artist's cursor. */
  private live: { layerId: string; getPartial: () => StrokeOp | null } | null = null;

  constructor(
    private docW: number,
    private docH: number,
    /** Resolves an image layer's pixels; null while still loading. */
    private getImage: (versionId: string) => HTMLImageElement | null,
  ) {}

  setLiveStroke(layerId: string, getPartial: () => StrokeOp | null) {
    this.live = { layerId, getPartial };
  }

  clearLiveStroke() {
    this.live = null;
  }

  /** Returns the layer's raster, updating it incrementally where possible. */
  layerCanvas(layer: LayerState): HTMLCanvasElement {
    let e = this.entries.get(layer.id);
    if (!e) {
      const canvas = document.createElement("canvas");
      canvas.width = this.docW;
      canvas.height = this.docH;
      e = { canvas, ctx: canvas.getContext("2d")!, rasterized: [], imageDrawn: false, cleared: true };
      this.entries.set(layer.id, e);
    }

    if (layer.kind === "text" && layer.text && typeof layer.text.content === "string") {
      // Text re-rasterizes when its props change; the JSON doubles as the
      // cache key via the rasterized list (one synthetic entry).
      const key = "text:" + JSON.stringify(layer.text);
      if (e.rasterized[0] !== key || e.rasterized.length !== 1) {
        this.entriesReset(e);
        e.ctx.fillStyle = layer.text.color;
        e.ctx.font = `${layer.text.size}px Inter, system-ui, sans-serif`;
        e.ctx.textBaseline = "top";
        layer.text.content.split("\n").forEach((line, i) => {
          e!.ctx.fillText(line, layer.text!.x, layer.text!.y + i * layer.text!.size * 1.25);
        });
        e.rasterized = [key];
      }
      return e.canvas;
    }
    const strokes = layer.strokes;
    let prefix = 0;
    while (
      prefix < e.rasterized.length &&
      prefix < strokes.length &&
      e.rasterized[prefix] === strokes[prefix].op_id
    ) {
      prefix++;
    }
    if (prefix < e.rasterized.length) {
      // History diverged (undo/remote) — rebuild this layer from scratch.
      e.ctx.clearRect(0, 0, this.docW, this.docH);
      e.rasterized = [];
      e.imageDrawn = false;
      e.cleared = true;
      prefix = 0;
    }

    if (layer.kind === "image" && layer.versionId && !e.imageDrawn) {
      const img = this.getImage(layer.versionId);
      // A masked layer draws only when BOTH pixels and mask are loaded —
      // an unmasked flash of the full candidate would be worse than a beat
      // of nothing.
      const mask = layer.maskVersionId ? this.getImage(layer.maskVersionId) : null;
      if (img && (!layer.maskVersionId || mask)) {
        if (mask) {
          // Masked (gen-fill) layers map candidate AND mask to doc dims —
          // real endpoints may round output dims, and a natural-size draw
          // under a doc-stretched mask would misalign.
          e.ctx.drawImage(img, 0, 0, this.docW, this.docH);
          e.ctx.save();
          e.ctx.globalCompositeOperation = "destination-in";
          e.ctx.drawImage(this.alphaMask(layer.maskVersionId!, mask), 0, 0, this.docW, this.docH);
          e.ctx.restore();
        } else {
          // Natural size, no stretch: the canvas is sized to the version's
          // dims on creation, and a dims-less fallback must not distort.
          e.ctx.drawImage(img, 0, 0);
        }
        e.imageDrawn = true;
      }
    }

    for (let i = prefix; i < strokes.length; i++) {
      drawStroke(e.ctx, strokes[i]);
      e.rasterized.push(strokes[i].op_id);
    }

    if (e.cleared) {
      e.cleared = false;
      if (this.live?.layerId === layer.id) {
        const partial = this.live.getPartial();
        if (partial && partial.points.length > 0) drawStroke(e.ctx, partial);
      }
    }
    return e.canvas;
  }

  private entriesReset(e: LayerEntry) {
    e.ctx.clearRect(0, 0, this.docW, this.docH);
    e.rasterized = [];
    e.imageDrawn = false;
    e.cleared = true;
  }

  /** The raw drawing surface for live (uncommitted) strokes. */
  liveContext(layerId: string): CanvasRenderingContext2D | undefined {
    return this.entries.get(layerId)?.ctx;
  }

  /** A live-drawn stroke was committed as an op: it is already on the layer
   * canvas, so record its op_id instead of re-rasterizing. */
  noteLiveStrokeCommitted(layerId: string, opId: string) {
    this.entries.get(layerId)?.rasterized.push(opId);
  }

  /** An image finished loading — let the layers that use it (as pixels OR
   * as mask) redraw. */
  invalidateImage(versionId: string, state: CanvasDocState) {
    for (const layer of state.layers) {
      if (layer.versionId !== versionId && layer.maskVersionId !== versionId) continue;
      const e = this.entries.get(layer.id);
      if (e && !e.imageDrawn) {
        // Force full rebuild so strokes land above the image.
        e.ctx.clearRect(0, 0, this.docW, this.docH);
        e.rasterized = [];
        e.cleared = true;
      }
    }
  }

  /** White = visible: converts a black/white mask image into an alpha mask
   * usable with destination-in. Cached per version (content-addressed). */
  private alphaMask(versionId: string, mask: HTMLImageElement): HTMLCanvasElement {
    let c = this.alphaMasks.get(versionId);
    if (c) return c;
    c = document.createElement("canvas");
    c.width = mask.naturalWidth;
    c.height = mask.naturalHeight;
    const ctx = c.getContext("2d")!;
    ctx.drawImage(mask, 0, 0);
    const data = ctx.getImageData(0, 0, c.width, c.height);
    const px = data.data;
    for (let i = 0; i < px.length; i += 4) {
      px[i + 3] = (px[i] + px[i + 1] + px[i + 2]) / 3;
    }
    ctx.putImageData(data, 0, 0);
    this.alphaMasks.set(versionId, c);
    return c;
  }

  /** Drop rasters for layers no longer in the doc (a 1080p layer holds ~8MB;
   * an undo of remove_layer just rebuilds from ops). No size-based early-out:
   * hidden layers never get entries, so counting would let discarded
   * gen-fill preview rasters leak. Alpha masks are pruned alongside. */
  evict(state: CanvasDocState) {
    const keep = new Set(state.layers.map((l) => l.id));
    if (this.live) keep.add(this.live.layerId);
    for (const id of [...this.entries.keys()]) {
      if (!keep.has(id)) this.entries.delete(id);
    }
    if (this.alphaMasks.size > 0) {
      const keepMasks = new Set(state.layers.map((l) => l.maskVersionId).filter(Boolean));
      for (const id of [...this.alphaMasks.keys()]) {
        if (!keepMasks.has(id)) this.alphaMasks.delete(id);
      }
    }
  }
}

export function drawStroke(ctx: CanvasRenderingContext2D, s: StrokeOp) {
  ctx.save();
  ctx.globalCompositeOperation = s.tool === "eraser" ? "destination-out" : "source-over";
  ctx.strokeStyle = s.color;
  ctx.fillStyle = s.color;
  ctx.lineWidth = s.size;
  ctx.lineCap = "round";
  ctx.lineJoin = "round";
  const pts = s.points;
  if (pts.length === 1) {
    ctx.beginPath();
    ctx.arc(pts[0][0], pts[0][1], s.size / 2, 0, Math.PI * 2);
    ctx.fill();
  } else if (pts.length > 1) {
    ctx.beginPath();
    ctx.moveTo(pts[0][0], pts[0][1]);
    for (let i = 1; i < pts.length; i++) ctx.lineTo(pts[i][0], pts[i][1]);
    ctx.stroke();
  }
  ctx.restore();
}

/** Draw one live segment of an in-progress stroke (same visual result as the
 * final full-path render for solid round strokes). */
export function drawLiveSegment(
  ctx: CanvasRenderingContext2D,
  s: Pick<StrokeOp, "tool" | "color" | "size">,
  from: [number, number],
  to: [number, number],
) {
  ctx.save();
  ctx.globalCompositeOperation = s.tool === "eraser" ? "destination-out" : "source-over";
  ctx.strokeStyle = s.color;
  ctx.lineWidth = s.size;
  ctx.lineCap = "round";
  ctx.beginPath();
  ctx.moveTo(from[0], from[1]);
  ctx.lineTo(to[0], to[1]);
  ctx.stroke();
  ctx.restore();
}

/** Composite all visible layers onto the target under the view transform. */
export function composite(
  target: HTMLCanvasElement,
  state: CanvasDocState,
  cache: LayerRasterCache,
  vt: ViewTransform,
  docW: number,
  docH: number,
  dpr: number,
) {
  const ctx = target.getContext("2d")!;
  ctx.setTransform(1, 0, 0, 1, 0, 0);
  ctx.clearRect(0, 0, target.width, target.height);
  ctx.setTransform(vt.scale * dpr, 0, 0, vt.scale * dpr, vt.x * dpr, vt.y * dpr);
  ctx.imageSmoothingEnabled = vt.scale < 3; // crisp pixels at high zoom
  ctx.fillStyle = "#ffffff";
  ctx.fillRect(0, 0, docW, docH);
  for (const layer of state.layers) {
    if (!layer.visible || layer.opacity === 0) continue;
    ctx.globalAlpha = layer.opacity;
    ctx.drawImage(cache.layerCanvas(layer), 0, 0);
  }
  ctx.globalAlpha = 1;
  cache.evict(state);
}

/** Flatten at full doc resolution (export path — same pipeline, identity view). */
export function flatten(state: CanvasDocState, cache: LayerRasterCache, docW: number, docH: number): HTMLCanvasElement {
  const out = document.createElement("canvas");
  out.width = docW;
  out.height = docH;
  composite(out, state, cache, { x: 0, y: 0, scale: 1 }, docW, docH, 1);
  return out;
}
