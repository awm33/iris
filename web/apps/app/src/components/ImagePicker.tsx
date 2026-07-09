import { useQuery } from "@tanstack/react-query";
import { AssetKind } from "@iris/api-client";
import { assetClient } from "../api";
import { AssetThumb } from "./AssetThumb";

// Modal-ish library image picker, reused by promote flows, view creation,
// and character refs.
export function ImagePicker(props: {
  projectId?: string;
  title: string;
  onPick: (assetId: string, name: string) => void;
  onClose: () => void;
}) {
  const assets = useQuery({
    queryKey: ["assets", props.projectId ?? "", "image-picker"],
    queryFn: () => assetClient.listAssets({ projectId: props.projectId ?? "", kind: AssetKind.IMAGE }),
  });
  return (
    <div className="overlay" onClick={props.onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <div className="panel-header">
          <h3>{props.title}</h3>
          <button className="btn secondary" onClick={props.onClose}>
            Cancel
          </button>
        </div>
        {assets.data?.assets.length === 0 && <div className="empty">No images in this project yet.</div>}
        <div className="picker-grid">
          {assets.data?.assets.map((a) => (
            <button key={a.id} className="picker-cell" onClick={() => props.onPick(a.id, a.name)}>
              <AssetThumb assetId={a.id} className="picker-thumb" />
              <span className="picker-name">{a.name}</span>
            </button>
          ))}
        </div>
      </div>
    </div>
  );
}
