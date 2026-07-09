import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { JobState, type Shot } from "@iris/api-client";
import { generationClient, storyClient } from "../api";
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
  onGenerateForShot: (shotId: string, label: string, recipeJson?: string) => void;
}) {
  const qc = useQueryClient();
  const [addingView, setAddingView] = useState(false);
  const [shotDesc, setShotDesc] = useState("");

  const scene = useQuery({
    queryKey: ["scene", props.sceneId],
    queryFn: () => storyClient.getScene({ id: props.sceneId }),
    // Slow-poll backstop (same principle as jobs): takes arriving while the
    // SSE stream is down must still appear.
    refetchInterval: 30_000,
  });
  // Active jobs targeting shots drive the ⟳ generating badges (shares the
  // App-level jobs cache entry).
  const jobs = useQuery({
    queryKey: ["jobs", props.projectId],
    queryFn: () => generationClient.listJobs({ projectId: props.projectId }),
  });
  const generatingShots = new Set(
    (jobs.data?.jobs ?? [])
      .filter(
        (j) =>
          (j.state === JobState.QUEUED || j.state === JobState.DISPATCHED || j.state === JobState.RUNNING) &&
          j.targetEntityId !== "",
      )
      .map((j) => j.targetEntityId),
  );

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

  const submitShot = () => {
    if (shotDesc.trim() && !createShot.isPending) createShot.mutate(shotDesc.trim());
  };

  if (scene.isError) {
    return (
      <div>
        <div className="toolbar">
          <button className="btn secondary" onClick={props.onBack}>
            ← Scenes
          </button>
        </div>
        <div className="empty">
          Failed to load this scene — it may have been deleted.
          <div style={{ marginTop: 12 }}>
            <button className="btn secondary" onClick={() => scene.refetch()}>
              Retry
            </button>
          </div>
        </div>
      </div>
    );
  }
  const sc = scene.data?.scene;
  if (!sc) return <div className="empty">Loading scene…</div>;

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
        {(addView.isError || removeView.isError) && (
          <div className="status error">{String(addView.error ?? removeView.error)}</div>
        )}
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
                <button
                  className="chip-x"
                  title="Remove view"
                  disabled={removeView.isPending}
                  onClick={() => {
                    if (
                      window.confirm(
                        `Remove view "${v.name}"? Shots framing it will be detached (the image stays in the library).`,
                      )
                    ) {
                      removeView.mutate(v.id);
                    }
                  }}
                >
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
            onKeyDown={(e) => e.key === "Enter" && submitShot()}
            style={{ flex: 1, maxWidth: 520 }}
          />
          <button className="btn" disabled={!shotDesc.trim() || createShot.isPending} onClick={submitShot}>
            Add shot
          </button>
        </div>
        {createShot.isError && <div className="status error">{String(createShot.error)}</div>}
        {sc.shots.length === 0 && <div className="empty">No shots yet — write the scene as beats, one shot each.</div>}
        <div className="shot-list">
          {sc.shots.map((sh, i) => (
            <ShotCard
              key={sh.id}
              index={i}
              shot={sh}
              sceneId={props.sceneId}
              generating={generatingShots.has(sh.id)}
              onGenerate={(recipeJson) =>
                props.onGenerateForShot(
                  sh.id,
                  `Shot ${i + 1}${sh.description ? ` · ${truncate(sh.description, 40)}` : ""}`,
                  recipeJson,
                )
              }
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

function ShotCard(props: {
  index: number;
  shot: Shot;
  sceneId: string;
  generating: boolean;
  onGenerate: (recipeJson?: string) => void;
}) {
  const qc = useQueryClient();
  const [pickingTakes, setPickingTakes] = useState(false);
  const sh = props.shot;

  const del = useMutation({
    mutationFn: () => storyClient.deleteShot({ id: sh.id }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["scene", props.sceneId] }),
  });

  const confirmDelete = () => {
    // Deleting a shot destroys its takes (recipes, selection) irreversibly;
    // the artifacts stay in the library. UX doc §4: destructive actions with
    // dependents must confirm and name them.
    const what =
      sh.takeCount > 0
        ? `Delete Shot ${props.index + 1} and its ${sh.takeCount} take${sh.takeCount > 1 ? "s" : ""}? Take recipes and selection are lost; the media stays in the library.`
        : `Delete Shot ${props.index + 1}?`;
    if (window.confirm(what)) del.mutate();
  };

  return (
    <div className="shot-card">
      {sh.selectedTakeVersionId ? (
        <VersionThumb versionId={sh.selectedTakeVersionId} className="shot-thumb" />
      ) : (
        <div className="shot-thumb thumb-placeholder-sm">▢</div>
      )}
      <div className="shot-main">
        <div className="name truncate">
          Shot {props.index + 1}
          {sh.description ? ` · ${sh.description}` : ""}
        </div>
        <div className="meta">
          {sh.takeCount > 0 ? `${sh.takeCount} take${sh.takeCount > 1 ? "s" : ""}` : "no takes"}
          {sh.selectedTakeId ? " · ✓ selected" : ""}
          {props.generating ? " · ⟳ generating" : ""}
          {sh.continuityStale ? " · ⚠ stale" : ""}
        </div>
        {del.isError && <div className="status error">{String(del.error)}</div>}
      </div>
      <div className="shot-actions">
        <button className="btn" onClick={() => props.onGenerate()}>
          ⚡ Generate takes
        </button>
        <button className="btn secondary" disabled={sh.takeCount === 0} onClick={() => setPickingTakes(true)}>
          Takes ▾
        </button>
        <button className="btn secondary" title="Delete shot" disabled={del.isPending} onClick={confirmDelete}>
          🗑
        </button>
      </div>
      {pickingTakes && (
        <TakePicker
          shotId={sh.id}
          selectedTakeId={sh.selectedTakeId}
          onRegenerate={(recipeJson) => {
            setPickingTakes(false);
            props.onGenerate(recipeJson);
          }}
          onClose={() => setPickingTakes(false)}
        />
      )}
    </div>
  );
}

export function truncate(s: string, n: number): string {
  const r = [...s];
  return r.length > n ? r.slice(0, n).join("") + "…" : s;
}
