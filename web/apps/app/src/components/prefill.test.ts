import { describe, expect, it } from "vitest";
import { prefillFromRecipe } from "./GeneratePanel";

// Recipe-replay fidelity: ♻ Regenerate must reconstruct EVERYTHING the
// original request carried — dropping the audio ref or the voice param
// silently regenerates a different thing (the dialogue-workflow bugs the
// PR 31 review caught).

const dialogueRecipe = JSON.stringify({
  endpoint_id: "mep_1",
  task: "i2v",
  profile: "draft",
  request: {
    prompt: "Mara talks",
    output: { duration_s: 4 },
    references: [
      { kind: "image", role: "character", asset_id: "ast_img" },
      { kind: "audio", role: "speech_lipsync", asset_id: "ast_voice" },
    ],
    params: { voice_id: "mara", extra_num: 3 },
  },
});

describe("prefillFromRecipe", () => {
  it("carries ALL reference kinds, not just image", () => {
    const p = prefillFromRecipe(dialogueRecipe)!;
    expect(p.refs).toHaveLength(2);
    expect(p.refs![1]).toMatchObject({ assetId: "ast_voice", role: "speech_lipsync", kind: "audio" });
    expect(p.refs![0].kind).toBe("image");
  });

  it("carries string params (voice) and drops non-strings", () => {
    const p = prefillFromRecipe(dialogueRecipe)!;
    expect(p.params).toEqual({ voice_id: "mara" });
  });

  it("carries conditioning.source_video (lipsync replay)", () => {
    const p = prefillFromRecipe(
      JSON.stringify({
        endpoint_id: "e",
        task: "lipsync_post",
        request: { conditioning: { source_video: { asset_id: "ast_v", version_id: "astv_v" } } },
      }),
    )!;
    expect(p.sourceVideo).toEqual({ assetId: "ast_v", versionId: "astv_v" });
  });

  it("omits params when none are strings", () => {
    const p = prefillFromRecipe(JSON.stringify({ endpoint_id: "e", request: { params: { n: 1 } } }))!;
    expect(p.params).toBeUndefined();
  });
});
