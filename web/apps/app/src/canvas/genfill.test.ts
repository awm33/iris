import { describe, expect, it } from "vitest";
import type { ModelEndpoint } from "@iris/api-client";
import { genFillEndpoints, pickProfile, pickRemovalEndpoint, removalDilation } from "./genfill";

const ep = (id: string, manifest: object, healthy = true): ModelEndpoint =>
  ({ id, displayName: id, healthy, manifestJson: JSON.stringify(manifest) }) as ModelEndpoint;

const inpaintManifest = (extra: object = {}) => ({
  tasks: ["inpaint"],
  conditioning: { mask: true, source_image: true },
  profiles: { draft: { max_width: 1024, max_height: 1024 }, master: { max_width: 4096, max_height: 4096 } },
  ...extra,
});

describe("genFillEndpoints", () => {
  it("offers only inpaint+mask+source_image endpoints; specialists flagged", () => {
    const out = genFillEndpoints([
      ep("gen", inpaintManifest()),
      ep("lama", inpaintManifest({ features: { prompt: false } })),
      ep("video", { tasks: ["t2v"], conditioning: { mask: true } }),
      ep("dead", inpaintManifest(), false),
      { id: "broken", displayName: "broken", healthy: true, manifestJson: "{nope" } as ModelEndpoint,
    ]);
    expect(out.map((e) => e.id)).toEqual(["gen", "lama"]);
    expect(out[0].promptable).toBe(true); // features.prompt omitted → true
    expect(out[1].promptable).toBe(false);
  });
});

describe("pickProfile", () => {
  it("picks the cheapest fitting profile; null when nothing fits", () => {
    const [e] = genFillEndpoints([ep("m", inpaintManifest())]);
    expect(pickProfile(e, 800, 600)).toBe("draft");
    expect(pickProfile(e, 1880, 1058)).toBe("master");
    expect(pickProfile(e, 9000, 100)).toBeNull();
  });
});

describe("pickRemovalEndpoint", () => {
  const endpoints = genFillEndpoints([
    ep("gen", inpaintManifest()),
    ep("lama", inpaintManifest({ features: { prompt: false } })),
  ]);

  it("prefers a fitting specialist (fast tier)", () => {
    expect(pickRemovalEndpoint(endpoints, 800, 600)?.id).toBe("lama");
  });

  it("falls back to a promptable endpoint when the specialist does not fit", () => {
    const small = genFillEndpoints([
      ep("gen", inpaintManifest()),
      ep("lama", {
        tasks: ["inpaint"],
        conditioning: { mask: true, source_image: true },
        features: { prompt: false },
        profiles: { draft: { max_width: 512, max_height: 512 } },
      }),
    ]);
    expect(pickRemovalEndpoint(small, 1880, 1058)?.id).toBe("gen");
  });

  it("undefined when nothing fits", () => {
    expect(pickRemovalEndpoint(endpoints, 9999, 9999)).toBeUndefined();
  });
});

describe("removalDilation", () => {
  it("scales with the selection and stays bounded", () => {
    expect(removalDilation({ kind: "rect", x: 0, y: 0, w: 50, h: 50 })).toBe(8); // floor
    expect(removalDilation({ kind: "rect", x: 0, y: 0, w: 500, h: 100 })).toBe(20); // 4% of max dim
    expect(removalDilation({ kind: "rect", x: 0, y: 0, w: 4000, h: 4000 })).toBe(48); // cap
    expect(
      removalDilation({
        kind: "lasso",
        points: [
          [10, 10],
          [310, 10],
          [310, 110],
        ],
      }),
    ).toBe(12);
  });
});
