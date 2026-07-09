// Gen-fill support: selection geometry, mask rendering, endpoint capability
// parsing. The mask contract follows spec/inference-api.md — white = generate,
// black = preserve.
import type { ModelEndpoint } from "@iris/api-client";

export type Selection =
  | { kind: "rect"; x: number; y: number; w: number; h: number }
  | { kind: "lasso"; points: [number, number][] }
  // Subject select (SAM): an object-shaped mask bitmap at doc resolution.
  // maskCanvas is white-on-black (the inpaint convention); tintCanvas is the
  // same shape pre-tinted for the viewport overlay.
  | {
      kind: "bitmap";
      maskCanvas: HTMLCanvasElement;
      tintCanvas: HTMLCanvasElement;
      bbox: { x: number; y: number; w: number; h: number };
    };

/** Trace a path-based selection (rect/lasso) into the current path.
 * Bitmap selections have no path — callers branch on kind. */
export function traceSelection(ctx: CanvasRenderingContext2D, sel: Selection) {
  ctx.beginPath();
  if (sel.kind === "rect") {
    ctx.rect(sel.x, sel.y, sel.w, sel.h);
  } else if (sel.kind === "lasso") {
    const pts = sel.points;
    if (pts.length === 0) return;
    ctx.moveTo(pts[0][0], pts[0][1]);
    for (let i = 1; i < pts.length; i++) ctx.lineTo(pts[i][0], pts[i][1]);
    ctx.closePath();
  }
}

/** Decode a SAM mask PNG into a bitmap Selection (bbox + overlay tint). */
export async function bitmapSelectionFromMask(png: Uint8Array, docW: number, docH: number): Promise<Selection | undefined> {
  const bmp = await createImageBitmap(new Blob([png as BlobPart], { type: "image/png" }));
  const maskCanvas = document.createElement("canvas");
  maskCanvas.width = docW;
  maskCanvas.height = docH;
  const mctx = maskCanvas.getContext("2d")!;
  mctx.fillStyle = "#000";
  mctx.fillRect(0, 0, docW, docH);
  mctx.drawImage(bmp, 0, 0, docW, docH);

  const data = mctx.getImageData(0, 0, docW, docH).data;
  const tintCanvas = document.createElement("canvas");
  tintCanvas.width = docW;
  tintCanvas.height = docH;
  const tctx = tintCanvas.getContext("2d")!;
  const tint = tctx.createImageData(docW, docH);
  let x0 = docW, y0 = docH, x1 = -1, y1 = -1;
  for (let i = 0, p = 0; i < data.length; i += 4, p++) {
    if (data[i] > 127) {
      const x = p % docW, y = (p / docW) | 0;
      if (x < x0) x0 = x;
      if (x > x1) x1 = x;
      if (y < y0) y0 = y;
      if (y > y1) y1 = y;
      tint.data[i] = 139; // iris violet
      tint.data[i + 1] = 124;
      tint.data[i + 2] = 246;
      tint.data[i + 3] = 110;
    }
  }
  if (x1 < 0) return undefined; // empty mask — nothing selected
  tctx.putImageData(tint, 0, 0);
  return { kind: "bitmap", maskCanvas, tintCanvas, bbox: { x: x0, y: y0, w: x1 - x0 + 1, h: y1 - y0 + 1 } };
}

/** Render the selection as a black/white mask PNG at doc resolution.
 * dilatePx > 0 grows the mask outward (stroke + fill ≈ morphological
 * dilation) — removal wants this (Photoshop does it too): it covers object
 * fringes and slight under-selection, and gives the model clean background
 * as boundary context instead of amputated object edges. */
export function renderMaskBlob(sel: Selection, docW: number, docH: number, dilatePx = 0): Promise<Blob> {
  const c = document.createElement("canvas");
  c.width = docW;
  c.height = docH;
  const ctx = c.getContext("2d")!;
  ctx.fillStyle = "#000";
  ctx.fillRect(0, 0, docW, docH);
  if (sel.kind === "bitmap") {
    ctx.drawImage(sel.maskCanvas, 0, 0);
    if (dilatePx > 0) {
      // Approximate dilation: re-draw the (white-on-black) mask offset in a
      // ring; "lighten" keeps white white.
      ctx.globalCompositeOperation = "lighten";
      for (let a = 0; a < 16; a++) {
        const t = (a / 16) * Math.PI * 2;
        ctx.drawImage(sel.maskCanvas, Math.cos(t) * dilatePx, Math.sin(t) * dilatePx);
      }
      ctx.globalCompositeOperation = "source-over";
    }
  } else {
    ctx.fillStyle = "#fff";
    traceSelection(ctx, sel);
    ctx.fill();
    if (dilatePx > 0) {
      ctx.strokeStyle = "#fff";
      ctx.lineWidth = dilatePx * 2;
      ctx.lineJoin = "round";
      ctx.lineCap = "round";
      traceSelection(ctx, sel);
      ctx.stroke();
    }
  }
  return new Promise((resolve, reject) => {
    c.toBlob((b) => (b ? resolve(b) : reject(new Error("mask encode failed"))), "image/png");
  });
}

/** Removal dilation: scale with the selection, bounded sanely. */
export function removalDilation(sel: Selection): number {
  let w = 0;
  let h = 0;
  if (sel.kind === "rect") {
    w = sel.w;
    h = sel.h;
  } else if (sel.kind === "bitmap") {
    w = sel.bbox.w;
    h = sel.bbox.h;
  } else {
    const xs = sel.points.map((p) => p[0]);
    const ys = sel.points.map((p) => p[1]);
    w = Math.max(...xs) - Math.min(...xs);
    h = Math.max(...ys) - Math.min(...ys);
  }
  return Math.min(48, Math.max(8, Math.round(Math.max(w, h) * 0.04)));
}

export interface GenFillEndpoint {
  id: string;
  name: string;
  /** features.prompt !== false — prompt-ignoring specialists (removal-only
   * inpainters like LaMa) are never offered for prompted generation, and are
   * PREFERRED for Remove (they're the fast tier). */
  promptable: boolean;
  profiles: { name: string; maxW: number; maxH: number }[];
}

/** Endpoints able to serve gen-fill: task `inpaint` plus mask + source_image
 * conditioning (manifest honesty rule — undeclared = unsupported). */
export function genFillEndpoints(endpoints: ModelEndpoint[]): GenFillEndpoint[] {
  const out: GenFillEndpoint[] = [];
  for (const ep of endpoints) {
    if (!ep.healthy || !ep.manifestJson) continue;
    try {
      const m = JSON.parse(ep.manifestJson) as {
        tasks?: string[];
        conditioning?: { mask?: boolean; source_image?: boolean };
        features?: { prompt?: boolean };
        profiles?: Record<string, { max_width?: number; max_height?: number }>;
      };
      if (!m.tasks?.includes("inpaint") || !m.conditioning?.mask || !m.conditioning?.source_image) continue;
      const profiles = Object.entries(m.profiles ?? {}).map(([name, p]) => ({
        name,
        maxW: p.max_width ?? 0,
        maxH: p.max_height ?? 0,
      }));
      out.push({ id: ep.id, name: ep.displayName, promptable: m.features?.prompt !== false, profiles });
    } catch {
      /* unparseable manifest → not offered */
    }
  }
  return out;
}

/** Removal routing (Photoshop Mode:Auto spirit): prefer a prompt-ignoring
 * specialist that fits the canvas; fall back to any fitting endpoint. */
export function pickRemovalEndpoint(
  endpoints: GenFillEndpoint[],
  w: number,
  h: number,
): GenFillEndpoint | undefined {
  const fits = (e: GenFillEndpoint) => pickProfile(e, w, h) !== null;
  return endpoints.find((e) => !e.promptable && fits(e)) ?? endpoints.find(fits);
}

/** Cheapest profile that fits the canvas; null when none does. */
export function pickProfile(ep: GenFillEndpoint, w: number, h: number): string | null {
  const fitting = ep.profiles
    .filter((p) => p.maxW >= w && p.maxH >= h)
    .sort((a, b) => a.maxW * a.maxH - b.maxW * b.maxH);
  return fitting[0]?.name ?? null;
}
