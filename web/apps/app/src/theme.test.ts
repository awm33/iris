import { describe, expect, it } from "vitest";
import { ACCENT_FALLBACK, parseColor, tokenRGB } from "./theme";

describe("parseColor", () => {
  it("reads the token formats a stylesheet can hold", () => {
    expect(parseColor(" #8b7cf6 ")).toEqual([139, 124, 246]);
    expect(parseColor("#fff")).toEqual([255, 255, 255]);
    expect(parseColor("rgb(139, 124, 246)")).toEqual([139, 124, 246]);
    expect(parseColor("rgba(139 124 246 / 0.5)")).toEqual([139, 124, 246]);
  });
  it("rejects what it can't resolve (caller falls back)", () => {
    expect(parseColor("")).toBeUndefined();
    expect(parseColor("var(--accent)")).toBeUndefined();
    expect(parseColor("oklch(70% 0.2 290)")).toBeUndefined();
  });
});

describe("tokenRGB", () => {
  it("falls back outside a DOM", () => {
    expect(tokenRGB("--accent", ACCENT_FALLBACK)).toEqual(ACCENT_FALLBACK);
  });
});
