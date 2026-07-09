import { newOpId } from "@iris/doc-runtime";
import { assetClient, canvasClient } from "../api";

/**
 * "Edit in canvas": new canvas sized to the image's head version, with the
 * image as an immutable base layer (referenced by content-addressed version —
 * the canvas doc never copies pixels).
 */
export async function createCanvasFromAsset(projectId: string, assetId: string): Promise<string> {
  const { asset, versions } = await assetClient.getAsset({ id: assetId });
  if (!asset) throw new Error("asset not found");
  const head = versions.find((v) => v.id === asset.headVersionId) ?? versions[0];
  if (!head) throw new Error("asset has no versions");
  const width = head.width || 1920;
  const height = head.height || 1080;

  const created = await canvasClient.createCanvas({ projectId, name: asset.name, width, height });
  const canvas = created.canvas!;
  await canvasClient.appendOps({
    canvasId: canvas.id,
    baseSeq: 0n,
    payloads: [
      JSON.stringify({
        op_id: newOpId(),
        type: "add_layer",
        layer: { id: `lyr_${newOpId().slice(3)}`, name: asset.name, kind: "image", version_id: head.id },
      }),
      // Start with a paint layer on top so the first brush stroke just works.
      JSON.stringify({
        op_id: newOpId(),
        type: "add_layer",
        layer: { id: `lyr_${newOpId().slice(3)}`, name: "Layer 1", kind: "paint" },
      }),
    ],
  });
  return canvas.id;
}
