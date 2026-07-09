import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { AssetService, GenerationService, StoryService, WorkspaceService } from "@iris/api-client";

// Same-origin; vite proxies /iris.v1* to the Go api in dev.
const transport = createConnectTransport({ baseUrl: "/" });

export const workspaceClient = createClient(WorkspaceService, transport);
export const assetClient = createClient(AssetService, transport);
export const generationClient = createClient(GenerationService, transport);
export const storyClient = createClient(StoryService, transport);

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
