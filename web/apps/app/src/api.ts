import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { AssetService, CanvasService, GenerationService, StoryService, WorkspaceService } from "@iris/api-client";

// Same-origin; vite proxies /iris.v1* to the Go api in dev.
const transport = createConnectTransport({ baseUrl: "/" });

export const workspaceClient = createClient(WorkspaceService, transport);
export const assetClient = createClient(AssetService, transport);
export const generationClient = createClient(GenerationService, transport);
export const storyClient = createClient(StoryService, transport);
export const canvasClient = createClient(CanvasService, transport);

// keepalive lets a final autosave batch survive tab close/hide (browsers
// kill ordinary fetches on unload). Only for small unload-time appends —
// keepalive bodies are capped at 64KB by the platform.
const keepaliveTransport = createConnectTransport({
  baseUrl: "/",
  fetch: (input, init) => fetch(input, { ...init, keepalive: true }),
});
export const canvasKeepaliveClient = createClient(CanvasService, keepaliveTransport);

/** Full presigned upload flow: StartUpload → PUT bytes → CompleteUpload. */
export async function uploadFile(file: File, projectId?: string) {
  const start = await assetClient.startUpload({
    projectId: projectId ?? "",
    filename: file.name,
    contentType: file.type || "application/octet-stream",
    sizeBytes: BigInt(file.size),
  });
  const putUrl = start.partPutUrls[0];
  if (!putUrl) throw new Error("no upload URL returned");
  const res = await fetch(putUrl, {
    method: "PUT",
    body: file,
    headers: { "Content-Type": file.type || "application/octet-stream" },
  });
  if (!res.ok) throw new Error(`upload PUT failed: ${res.status}`);
  return assetClient.completeUpload({ uploadId: start.uploadId, etags: [] });
}
