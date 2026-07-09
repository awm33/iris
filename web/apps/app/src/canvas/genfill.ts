// Gen-fill support: selection geometry, mask rendering, endpoint capability
// parsing. The mask contract follows spec/inference-api.md — white = generate,
// black = preserve.
import type { ModelEndpoint } from "@iris/api-client";

export type Selection =
  | { kind: "rect"; x: number; y: number; w: number; h: number }
  | { kind: "lasso"; points: [number, number][] };

/** Trace the selection into the current path (doc coordinates). */
export function traceSelection(ctx: CanvasRenderingContext2D, sel: Selection) {
  ctx.beginPath();
  if (sel.kind === "rect") {
    ctx.rect(sel.x, sel.y, sel.w, sel.h);
  } else {
    const pts = sel.points;
    if (pts.length === 0) return;
    ctx.moveTo(pts[0][0], pts[0][1]);
    for (let i = 1; i < pts.length; i++) ctx.lineTo(pts[i][0], pts[i][1]);
    ctx.closePath();
  }
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
