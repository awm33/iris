import { Code, ConnectError } from "@connectrpc/connect";
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
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
  const [search, setSearch] = useState("");
  const [showUtility, setShowUtility] = useState(false);

  const assets = useQuery({
    queryKey: ["assets", props.projectId ?? "", search],
    // Typing must not unmount the grid to "Loading…" between keystrokes
    // (which would also collapse every expanded stack).
    placeholderData: keepPreviousData,
    queryFn: () => assetClient.listAssets({ projectId: props.projectId ?? "", query: search }),
  });

  // Workflow intermediates (gen-fill source/mask flattens) are tagged
  // "utility" at upload — real work, not library content. Hidden by default.
  const all = assets.data?.assets ?? [];
  const utilityCount = all.filter((a) => a.tags.includes("utility")).length;
  const visible = showUtility ? all : all.filter((a) => !a.tags.includes("utility"));

  // Fan-out candidates share a source job; stack them behind one card so a
  // ×8 run reads as one row, not eight identical thumbnails.
  const groups: { key: string; items: Asset[] }[] = [];
  {
    const byJob = new Map<string, { key: string; items: Asset[] }>();
    for (const a of visible) {
      const existing = a.sourceJobId ? byJob.get(a.sourceJobId) : undefined;
      if (existing) {
        existing.items.push(a);
        continue;
      }
      const g = { key: a.sourceJobId || a.id, items: [a] };
      groups.push(g);
      if (a.sourceJobId) byJob.set(a.sourceJobId, g);
    }
  }

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
        <input
          type="search"
          placeholder="Search library…"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          aria-label="Search library"
        />
        {utilityCount > 0 && (
          <button
            className="btn secondary"
            title="Gen-fill source/mask uploads and other workflow files"
            onClick={() => setShowUtility((v) => !v)}
          >
            {showUtility ? "Hide" : "Show"} utility files ({utilityCount})
          </button>
        )}
        {upload.isError && <span className="status error">upload failed: {String(upload.error)}</span>}
      </div>

      {assets.isLoading && <div className="empty">Loading…</div>}
      {assets.data && visible.length === 0 && (
        <div className="empty">
          {search
            ? "No matches — try a different search."
            : utilityCount > 0
              ? `${utilityCount} utility file${utilityCount > 1 ? "s" : ""} hidden — nothing else here yet.`
              : "Library is empty — upload an image, video, or audio file."}
        </div>
      )}
      <div className="grid">
        {groups.map((g) =>
          g.items.length === 1 ? (
            <AssetCard key={g.key} asset={g.items[0]} projectId={props.projectId} onEditInCanvas={props.onEditInCanvas} />
          ) : (
            <AssetStack key={g.key} assets={g.items} projectId={props.projectId} onEditInCanvas={props.onEditInCanvas} />
          ),
        )}
      </div>
      {stockOpen && props.projectId && <StockPicker projectId={props.projectId} onClose={() => setStockOpen(false)} />}
    </div>
  );
}

// Candidates from one generation job: one card until expanded.
function AssetStack(props: {
  assets: Asset[];
  projectId?: string;
  onEditInCanvas?: (assetId: string) => void;
}) {
  const [open, setOpen] = useState(false);
  if (!open) {
    return (
      <AssetCard
        asset={props.assets[0]}
        projectId={props.projectId}
        onEditInCanvas={props.onEditInCanvas}
        stack={{ count: props.assets.length, onExpand: () => setOpen(true) }}
      />
    );
  }
  return (
    <>
      {props.assets.map((a) => (
        <AssetCard key={a.id} asset={a} projectId={props.projectId} onEditInCanvas={props.onEditInCanvas} />
      ))}
      <div className="card">
        <button className="card-button" onClick={() => setOpen(false)}>
          <div className="thumb-placeholder">⌃</div>
          <div className="name">Collapse {props.assets.length} takes</div>
        </button>
      </div>
    </>
  );
}

function AssetCard({
  asset,
  projectId,
  onEditInCanvas,
  stack,
}: {
  asset: Asset;
  projectId?: string;
  onEditInCanvas?: (assetId: string) => void;
  stack?: { count: number; onExpand: () => void };
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
      {stack && (
        <div className="promote-row">
          <button className="btn secondary chip-add" onClick={stack.onExpand}>
            ▤ {stack.count} takes — show all
          </button>
        </div>
      )}
      {isVideo && asset.headVersionId && (
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
