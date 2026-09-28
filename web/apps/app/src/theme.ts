// Canvas drawing can't use CSS var(); resolve a design token at draw time so
// styles.css stays the single source of color. The fallback covers tests and
// any token value parseColor doesn't understand.
export type RGB = [number, number, number];

export const ACCENT_FALLBACK: RGB = [139, 124, 246];

// #rgb, #rrggbb, rgb()/rgba() (comma or space separated). Anything else → undefined.
export function parseColor(v: string): RGB | undefined {
  const s = v.trim();
  const hex = /^#([0-9a-f]{3}|[0-9a-f]{6})$/i.exec(s);
  if (hex) {
    const h = hex[1].length === 3 ? [...hex[1]].map((c) => c + c).join("") : hex[1];
    const n = parseInt(h, 16);
    return [(n >> 16) & 255, (n >> 8) & 255, n & 255];
  }
  const rgb = /^rgba?\(\s*(\d+)[\s,]+(\d+)[\s,]+(\d+)/i.exec(s);
  if (rgb) return [Number(rgb[1]), Number(rgb[2]), Number(rgb[3])];
  return undefined;
}

export function tokenRGB(name: string, fallback: RGB): RGB {
  if (typeof document === "undefined") return fallback;
  return parseColor(getComputedStyle(document.documentElement).getPropertyValue(name)) ?? fallback;
}
