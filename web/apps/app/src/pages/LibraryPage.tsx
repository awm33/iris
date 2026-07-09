import { Code, ConnectError } from "@connectrpc/connect";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { AssetKind, type Asset } from "@iris/api-client";
import { assetClient, storyClient, uploadFile } from "../api";
import { StockPicker } from "../components/StockPicker";
import { ClipPlayer } from "../components/ClipPlayer";

export function LibraryPage(props: {
  projectId?: string;
  onGenerate?: () => void;
  onEditInCanvas?: (assetId: string) => void;
}) {
  const qc = useQueryClient();
  const fileInput = useRef<HTMLInputElement>(null);
  const [stockOpen, setStockOpen] = useState(false);

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
        {props.onGenerate && (
          <button className="btn" onClick={props.onGenerate}>
            ⚡ Generate
          </button>
        )}
        <button className="btn secondary" onClick={() => fileInput.current?.click()} disabled={upload.isPending}>
          {upload.isPending ? "Uploading…" : "Upload media"}
        </button>
        {props.projectId && (
          <button className="btn secondary" onClick={() => setStockOpen(true)}>
            📷 Stock photos
          </button>
        )}
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
        {assets.data?.assets.map((a) => (
          <AssetCard key={a.id} asset={a} projectId={props.projectId} onEditInCanvas={props.onEditInCanvas} />
        ))}
      </div>
      {stockOpen && props.projectId && <StockPicker projectId={props.projectId} onClose={() => setStockOpen(false)} />}
    </div>
  );
}

function AssetCard({
  asset,
  projectId,
  onEditInCanvas,
}: {
  asset: Asset;
  projectId?: string;
  onEditInCanvas?: (assetId: string) => void;
}) {
  const qc = useQueryClient();
  const [promoting, setPromoting] = useState<"view" | "character" | null>(null);
  const [openingCanvas, setOpeningCanvas] = useState(false);
  const [playing, setPlaying] = useState(false);
  const isImage = asset.kind === AssetKind.IMAGE;
  const isVideo = asset.kind === AssetKind.VIDEO;

  const scenes = useQuery({
    queryKey: ["scenes", projectId ?? ""],
    enabled: promoting === "view" && !!projectId,
    queryFn: () => storyClient.listScenes({ projectId: projectId! }),
  });
  const characters = useQuery({
    queryKey: ["characters", projectId ?? ""],
    enabled: promoting === "character",
    queryFn: () => storyClient.listCharacters({ projectId: projectId ?? "" }),
  });
  const promoteView = useMutation({
    mutationFn: (sceneId: string) =>
      storyClient.addView({ sceneId, name: asset.name, plate: { assetId: asset.id, versionId: "" } }),
    onSuccess: () => {
      setPromoting(null);
      void qc.invalidateQueries({ queryKey: ["scene"] });
    },
  });
  const promoteCharacter = useMutation({
    mutationFn: (characterId: string) =>
      storyClient.addCharacterRef({ characterId, role: "turnaround", asset: { assetId: asset.id, versionId: "" } }),
    onSuccess: () => {
      setPromoting(null);
      void qc.invalidateQueries({ queryKey: ["characters"] });
    },
  });
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
      {isVideo && (
        <div className="promote-row">
          <button className="btn secondary chip-add" onClick={() => setPlaying(true)}>
            ▶ Play
          </button>
        </div>
      )}
      {playing && (
        <ClipPlayer versionId={asset.headVersionId} title={asset.name} onClose={() => setPlaying(false)} />
      )}
      {isImage && (
        <div className="promote-row">
          <button className="btn secondary chip-add" onClick={() => setPromoting("view")}>
            → View
          </button>
          <button className="btn secondary chip-add" onClick={() => setPromoting("character")}>
            → Character
          </button>
          {onEditInCanvas && (
            <button
              className="btn secondary chip-add"
              disabled={openingCanvas}
              onClick={() => {
                setOpeningCanvas(true);
                onEditInCanvas(asset.id);
              }}
            >
              {openingCanvas ? "…" : "🎨 Canvas"}
            </button>
          )}
        </div>
      )}
      {promoting === "view" && (
        <PromoteList
          title="Promote to view of…"
          loading={scenes.isLoading}
          pending={promoteView.isPending}
          items={(scenes.data?.scenes ?? []).map((s) => ({ id: s.id, label: s.name }))}
          emptyHint="No scenes yet — create one on the Scenes page."
          onPick={(id) => promoteView.mutate(id)}
          onClose={() => setPromoting(null)}
        />
      )}
      {promoting === "character" && (
        <PromoteList
          title="Add as turnaround ref for…"
          loading={characters.isLoading}
          pending={promoteCharacter.isPending}
          items={(characters.data?.characters ?? []).map((c) => ({ id: c.id, label: c.name }))}
          emptyHint="No characters yet — create one on the Characters page."
          onPick={(id) => promoteCharacter.mutate(id)}
          onClose={() => setPromoting(null)}
        />
      )}
      {(promoteView.isError || promoteCharacter.isError) && (
        <div className="status error">{String(promoteView.error ?? promoteCharacter.error)}</div>
      )}
    </div>
  );
}

function PromoteList(props: {
  title: string;
  loading: boolean;
  pending: boolean;
  items: { id: string; label: string }[];
  emptyHint: string;
  onPick: (id: string) => void;
  onClose: () => void;
}) {
  return (
    <div className="promote-list">
      <div className="meta">{props.title}</div>
      {props.loading && <div className="meta">Loading…</div>}
      {!props.loading && props.items.length === 0 && <div className="meta">{props.emptyHint}</div>}
      {props.items.map((it) => (
        <button
          key={it.id}
          className="btn secondary chip-add"
          disabled={props.pending}
          onClick={() => props.onPick(it.id)}
        >
          {it.label}
        </button>
      ))}
      <button className="chip-x" onClick={props.onClose}>
        ×
      </button>
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
