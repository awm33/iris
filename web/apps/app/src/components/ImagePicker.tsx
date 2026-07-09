import { useQuery } from "@tanstack/react-query";
import { AssetKind } from "@iris/api-client";
import { assetClient } from "../api";
import { useEscape, VersionThumb } from "./AssetThumb";

// Modal library image picker, reused by promote flows, view creation, and
// character refs. Renders head versions directly from the list response —
// no per-cell asset lookups.
export function ImagePicker(props: {
  projectId?: string;
  title: string;
  onPick: (assetId: string, name: string) => void;
  onClose: () => void;
}) {
  useEscape(props.onClose);
  const assets = useQuery({
    queryKey: ["assets", props.projectId ?? "", "image-picker"],
    queryFn: () => assetClient.listAssets({ projectId: props.projectId ?? "", kind: AssetKind.IMAGE }),
  });
  return (
    <div className="overlay" onClick={props.onClose}>
      <div className="modal" role="dialog" aria-modal="true" onClick={(e) => e.stopPropagation()}>
        <div className="panel-header">
          <h3>{props.title}</h3>
          <button className="btn secondary" onClick={props.onClose}>
            Cancel
          </button>
        </div>
        {assets.isLoading && <div className="empty">Loading library…</div>}
        {assets.isError && <div className="status error">Failed to load library: {String(assets.error)}</div>}
        {assets.data?.assets.length === 0 && <div className="empty">No images in this project yet.</div>}
        <div className="picker-grid">
          {assets.data?.assets.map((a) => (
            <button key={a.id} className="picker-cell" onClick={() => props.onPick(a.id, a.name)}>
              <VersionThumb versionId={a.headVersionId} kind="image" className="picker-thumb" />
              <span className="picker-name">{a.name}</span>
            </button>
          ))}
        </div>
      </div>
    </div>
  );
}
