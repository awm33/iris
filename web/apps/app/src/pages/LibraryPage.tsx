import { Code, ConnectError } from "@connectrpc/connect";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef } from "react";
import { AssetKind, type Asset } from "@iris/api-client";
import { assetClient, uploadFile } from "../api";

export function LibraryPage(props: { projectId?: string }) {
  const qc = useQueryClient();
  const fileInput = useRef<HTMLInputElement>(null);

  const assets = useQuery({
    queryKey: ["assets", props.projectId ?? ""],
    queryFn: () => assetClient.listAssets({ projectId: props.projectId ?? "" }),
  });

  const upload = useMutation({
    mutationFn: (file: File) => uploadFile(file, props.projectId),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["assets"] }),
  });

  return (
    <div>
      <h2>Library</h2>
      <div className="toolbar">
        <button className="btn" onClick={() => fileInput.current?.click()} disabled={upload.isPending}>
          {upload.isPending ? "Uploading…" : "Upload media"}
        </button>
        <input
          ref={fileInput}
          type="file"
          hidden
          accept="image/*,video/*,audio/*"
          onChange={(e) => {
            const f = e.target.files?.[0];
            if (f) upload.mutate(f);
            e.target.value = "";
          }}
        />
        {upload.isError && <span className="status">upload failed: {String(upload.error)}</span>}
      </div>

      {assets.isLoading && <div className="empty">Loading…</div>}
      {assets.data && assets.data.assets.length === 0 && (
        <div className="empty">Library is empty — upload an image, video, or audio file.</div>
      )}
      <div className="grid">
        {assets.data?.assets.map((a) => <AssetCard key={a.id} asset={a} />)}
      </div>
    </div>
  );
}

function AssetCard({ asset }: { asset: Asset }) {
  const isImage = asset.kind === AssetKind.IMAGE;
  const isVideo = asset.kind === AssetKind.VIDEO;
  const thumb = useQuery({
    queryKey: ["thumb", asset.headVersionId, isVideo ? "poster" : ""],
    enabled: (isImage || isVideo) && asset.headVersionId !== "",
    staleTime: 10 * 60 * 1000, // signed URLs live 15m
    // Video posters appear once the ingest probe runs. Retry ONLY NotFound
    // (poster-pending), long enough to cover queue latency (~2 min at 3s);
    // other errors fail fast instead of hammering the API.
    retry: (failureCount, error) =>
      isVideo && isNotFound(error) && failureCount < 40,
    retryDelay: 3000,
    queryFn: () =>
      assetClient.signDownload({
        versionId: asset.headVersionId,
        variant: isVideo ? "poster" : "",
      }),
  });

  return (
    <div className="card">
      {thumb.data ? (
        <img className="thumb" src={thumb.data.url} alt={asset.name} />
      ) : (
        <div className="thumb-placeholder">{kindGlyph(asset.kind)}</div>
      )}
      <div className="name">{asset.name}</div>
      <div className="meta">{AssetKind[asset.kind]?.toLowerCase() ?? "asset"}</div>
    </div>
  );
}

function isNotFound(error: unknown): boolean {
  return error instanceof ConnectError && error.code === Code.NotFound;
}

function kindGlyph(kind: AssetKind): string {
  switch (kind) {
    case AssetKind.VIDEO: return "🎬";
    case AssetKind.AUDIO: return "🎵";
    case AssetKind.MODEL_3D: return "▦";
    default: return "▢";
  }
}
