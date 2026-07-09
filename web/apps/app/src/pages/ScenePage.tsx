import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import type { Shot } from "@iris/api-client";
import { storyClient } from "../api";
import { AssetThumb, VersionThumb } from "../components/AssetThumb";
import { ImagePicker } from "../components/ImagePicker";
import { TakePicker } from "../components/TakePicker";

// The Scene page (UX doc §3.2, M3 slice): views strip with promote-from-
// library, shots with takes and generate-into-shot. 3D set and continuity
// chains arrive in later milestones.
export function ScenePage(props: {
  projectId: string;
  sceneId: string;
  onBack: () => void;
  onGenerateForShot: (shotId: string) => void;
}) {
  const qc = useQueryClient();
  const [addingView, setAddingView] = useState(false);
  const [shotDesc, setShotDesc] = useState("");

  const scene = useQuery({
    queryKey: ["scene", props.sceneId],
    queryFn: () => storyClient.getScene({ id: props.sceneId }),
  });

  const addView = useMutation({
    mutationFn: (p: { assetId: string; name: string }) =>
      storyClient.addView({ sceneId: props.sceneId, name: p.name, plate: { assetId: p.assetId, versionId: "" } }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["scene", props.sceneId] }),
  });
  const removeView = useMutation({
    mutationFn: (id: string) => storyClient.removeView({ id }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["scene", props.sceneId] }),
  });
  const createShot = useMutation({
    mutationFn: (description: string) => storyClient.createShot({ sceneId: props.sceneId, description }),
    onSuccess: () => {
      setShotDesc("");
      void qc.invalidateQueries({ queryKey: ["scene", props.sceneId] });
    },
  });

  const sc = scene.data?.scene;
  if (scene.isLoading || !sc) return <div className="empty">Loading scene…</div>;

  return (
    <div>
      <div className="toolbar">
        <button className="btn secondary" onClick={props.onBack}>
          ← Scenes
        </button>
        <h2 style={{ margin: 0 }}>{sc.name}</h2>
      </div>

      <section className="scene-section">
        <div className="section-head">
          <h3>Set · Views</h3>
          <button className="btn secondary" onClick={() => setAddingView(true)}>
            + Add view from library
          </button>
        </div>
        {sc.views.length === 0 && (
          <div className="empty">
            No views cataloged. Views are the set's reference plates — generate or upload an image, then promote it
            here to make it citable in every shot.
          </div>
        )}
        <div className="view-strip">
          {sc.views.map((v) => (
            <div key={v.id} className="view-card">
              <AssetThumb assetId={v.plate?.assetId ?? ""} className="view-thumb" />
              <div className="view-name">
                {v.name}
                <button className="chip-x" title="Remove view" onClick={() => removeView.mutate(v.id)}>
                  ×
                </button>
              </div>
            </div>
          ))}
        </div>
      </section>

      <section className="scene-section">
        <div className="section-head">
          <h3>Shots</h3>
        </div>
        <div className="toolbar">
          <input
            type="text"
            placeholder="New shot description… (e.g. Mara slides the plate across the counter)"
            value={shotDesc}
            onChange={(e) => setShotDesc(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && shotDesc.trim() && createShot.mutate(shotDesc.trim())}
            style={{ flex: 1, maxWidth: 520 }}
          />
          <button
            className="btn"
            disabled={!shotDesc.trim() || createShot.isPending}
            onClick={() => createShot.mutate(shotDesc.trim())}
          >
            Add shot
          </button>
        </div>
        {sc.shots.length === 0 && <div className="empty">No shots yet — write the scene as beats, one shot each.</div>}
        <div className="shot-list">
          {sc.shots.map((sh, i) => (
            <ShotCard
              key={sh.id}
              index={i}
              shot={sh}
              sceneId={props.sceneId}
              onGenerate={() => props.onGenerateForShot(sh.id)}
            />
          ))}
        </div>
      </section>

      {addingView && (
        <ImagePicker
          projectId={props.projectId}
          title="Promote a library image to a view"
          onPick={(assetId, name) => {
            addView.mutate({ assetId, name });
            setAddingView(false);
          }}
          onClose={() => setAddingView(false)}
        />
      )}
    </div>
  );
}

function ShotCard(props: { index: number; shot: Shot; sceneId: string; onGenerate: () => void }) {
  const qc = useQueryClient();
  const [pickingTakes, setPickingTakes] = useState(false);
  const sh = props.shot;

  const del = useMutation({
    mutationFn: () => storyClient.deleteShot({ id: sh.id }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["scene", props.sceneId] }),
  });

  return (
    <div className="shot-card">
      <SelectedTakeThumb shotId={sh.id} selectedTakeId={sh.selectedTakeId} />
      <div className="shot-main">
        <div className="name">
          Shot {props.index + 1}
          {sh.description ? ` · ${sh.description}` : ""}
        </div>
        <div className="meta">
          {sh.takeCount > 0 ? `${sh.takeCount} take${sh.takeCount > 1 ? "s" : ""}` : "no takes"}
          {sh.selectedTakeId ? " · ✓ selected" : ""}
          {sh.continuityStale ? " · ⚠ stale" : ""}
        </div>
      </div>
      <div className="shot-actions">
        <button className="btn" onClick={props.onGenerate}>
          ⚡ Generate takes
        </button>
        <button className="btn secondary" disabled={sh.takeCount === 0} onClick={() => setPickingTakes(true)}>
          Takes ▾
        </button>
        <button className="btn secondary" title="Delete shot" onClick={() => del.mutate()}>
          🗑
        </button>
      </div>
      {pickingTakes && (
        <TakePicker shotId={sh.id} selectedTakeId={sh.selectedTakeId} onClose={() => setPickingTakes(false)} />
      )}
    </div>
  );
}

// The shot card's face is its selected take.
function SelectedTakeThumb({ shotId, selectedTakeId }: { shotId: string; selectedTakeId: string }) {
  const takes = useQuery({
    queryKey: ["takes", shotId],
    enabled: selectedTakeId !== "",
    queryFn: () => storyClient.listTakes({ shotId }),
  });
  const selected = takes.data?.takes.find((t) => t.id === selectedTakeId);
  if (!selected) return <div className="shot-thumb thumb-placeholder-sm">▢</div>;
  return <VersionThumb versionId={selected.versionId} className="shot-thumb" />;
}
