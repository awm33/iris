// Canvas drawing can't use CSS var(); resolve a design token at draw time so
// styles.css stays the single source of color. The fallback covers tests and
// any token that isn't a plain #rrggbb.
export function tokenRGB(name: string, fallback: [number, number, number]): [number, number, number] {
  if (typeof document === "undefined") return fallback;
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  const m = /^#([0-9a-f]{6})$/i.exec(v);
  if (!m) return fallback;
  const n = parseInt(m[1], 16);
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255];
}

export const ACCENT_FALLBACK: [number, number, number] = [139, 124, 246];
