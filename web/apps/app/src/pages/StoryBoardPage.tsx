import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { JobState, type Shot } from "@iris/api-client";
import { generationClient, storyClient } from "../api";
import { VersionThumb } from "../components/AssetThumb";
import { TakePicker } from "../components/TakePicker";

// Story board (UX doc §3.1): the writers'-room board — scenes as columns,
// shots as cards with selected-take thumbs and state badges. Deferred from
// the spec: continuity-chain arrows + chain inspector (chains land with the
// carry-last-frame flow) and "Open in Timeline" (the timeline's Scene-shots
// picker already covers assembly).
export function StoryBoardPage(props: {
  projectId: string;
  onOpenScene: (sceneId: string) => void;
  onGenerateForShot: (shotId: string, label: string) => void;
}) {
  const qc = useQueryClient();
  const [newScene, setNewScene] = useState("");

  const scenes = useQuery({
    queryKey: ["scenes", props.projectId],
    queryFn: () => storyClient.listScenes({ projectId: props.projectId }),
  });
  // Active shot-targeted jobs drive ⟳ badges (shares the App jobs cache).
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

  const createScene = useMutation({
    mutationFn: (name: string) => storyClient.createScene({ projectId: props.projectId, name }),
    onSuccess: () => {
      setNewScene("");
      void qc.invalidateQueries({ queryKey: ["scenes", props.projectId] });
    },
  });
  const submitScene = () => {
    if (newScene.trim() && !createScene.isPending) createScene.mutate(newScene.trim());
  };

  if (scenes.isError) return <div className="status error">Couldn’t load the board: {String(scenes.error)}</div>;

  return (
    <div>
      <div className="toolbar">
        <h2 style={{ margin: 0 }}>Story</h2>
        <input
          type="text"
          placeholder="New scene name…"
          value={newScene}
          onChange={(e) => setNewScene(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && submitScene()}
        />
        <button className="btn secondary" disabled={!newScene.trim() || createScene.isPending} onClick={submitScene}>
          + Scene
        </button>
      </div>
      {createScene.isError && <div className="status error">{String(createScene.error)}</div>}
      {scenes.data?.scenes.length === 0 && (
        <div className="empty">No scenes yet — a scene is a place and a beat; shots live inside it.</div>
      )}
      <div className="board">
        {scenes.data?.scenes.map((sc, i) => (
          <SceneColumn
            key={sc.id}
            index={i}
            sceneId={sc.id}
            name={sc.name}
            generatingShots={generatingShots}
            onOpenScene={() => props.onOpenScene(sc.id)}
            onGenerateForShot={props.onGenerateForShot}
          />
        ))}
      </div>
    </div>
  );
}

function SceneColumn(props: {
  index: number;
  sceneId: string;
  name: string;
  generatingShots: Set<string>;
  onOpenScene: () => void;
  onGenerateForShot: (shotId: string, label: string) => void;
}) {
  const qc = useQueryClient();
  const [shotDesc, setShotDesc] = useState("");
  const [dragId, setDragId] = useState<string | null>(null);
  const [dropIndex, setDropIndex] = useState<number | null>(null);

  const scene = useQuery({
    queryKey: ["scene", props.sceneId],
    queryFn: () => storyClient.getScene({ id: props.sceneId }),
    // Slow-poll backstop, same as the Scene page: takes landing while the
    // SSE stream is down must still appear.
    refetchInterval: 30_000,
  });
  const shots = scene.data?.scene?.shots ?? [];

  const createShot = useMutation({
    mutationFn: (description: string) => storyClient.createShot({ sceneId: props.sceneId, description }),
    onSuccess: () => {
      setShotDesc("");
      void qc.invalidateQueries({ queryKey: ["scene", props.sceneId] });
    },
  });
  const reorder = useMutation({
    // Positions are plain ints with no server-side shifting: renumber every
    // shot whose position changed, then refetch the authoritative order.
    mutationFn: async (ordered: Shot[]) => {
      for (const [i, sh] of ordered.entries()) {
        if (sh.position !== i) await storyClient.updateShot({ id: sh.id, position: i });
      }
    },
    onSettled: () => void qc.invalidateQueries({ queryKey: ["scene", props.sceneId] }),
  });

  const submitShot = () => {
    if (shotDesc.trim() && !createShot.isPending) createShot.mutate(shotDesc.trim());
  };

  const drop = () => {
    if (dragId === null || dropIndex === null) return;
    const from = shots.findIndex((s) => s.id === dragId);
    setDragId(null);
    setDropIndex(null);
    if (from === -1 || reorder.isPending) return;
    // dropIndex is the gap index in the ORIGINAL list; removing the dragged
    // card first shifts gaps after it down by one.
    const to = dropIndex > from ? dropIndex - 1 : dropIndex;
    if (to === from) return;
    const next = [...shots];
    const [moved] = next.splice(from, 1);
    next.splice(to, 0, moved);
    reorder.mutate(next);
  };

  return (
    <div className="board-col">
      <button className="board-col-head card-button" onClick={props.onOpenScene} title="Open scene">
        <span className="name truncate">
          Scene {props.index + 1} · {props.name}
        </span>
        <span className="meta">
          {scene.data?.scene?.views.length ?? 0} views · {shots.length} shots
        </span>
      </button>
      {scene.isError && <div className="status error">Scene failed to load.</div>}
      {reorder.isError && <div className="status error">Reorder failed — order restored from the server.</div>}
      <div
        className="board-shots"
        onDragOver={(e) => {
          if (!dragId) return;
          e.preventDefault(); // required for drop to fire
        }}
        onDrop={drop}
        onDragLeave={(e) => {
          // Only clear when leaving the whole list, not moving between cards.
          if (!e.currentTarget.contains(e.relatedTarget as Node)) setDropIndex(null);
        }}
      >
        {shots.map((sh, i) => (
          <div key={sh.id}>
            {dropIndex === i && dragId && <div className="board-drop" />}
            <BoardShotCard
              index={i}
              shot={sh}
              sceneId={props.sceneId}
              generating={props.generatingShots.has(sh.id)}
              dragging={dragId === sh.id}
              onDragStart={() => setDragId(sh.id)}
              onDragEnd={() => {
                setDragId(null);
                setDropIndex(null);
              }}
              onDragOverCard={(before) => setDropIndex(before ? i : i + 1)}
              onGenerate={() =>
                props.onGenerateForShot(
                  sh.id,
                  `Shot ${i + 1}${sh.description ? ` · ${sh.description.slice(0, 40)}` : ""}`,
                )
              }
            />
          </div>
        ))}
        {dropIndex === shots.length && dragId && <div className="board-drop" />}
      </div>
      <div className="board-add">
        <input
          type="text"
          placeholder="+ shot…"
          value={shotDesc}
          onChange={(e) => setShotDesc(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && submitShot()}
        />
      </div>
      {createShot.isError && <div className="status error">{String(createShot.error)}</div>}
    </div>
  );
}

function BoardShotCard(props: {
  index: number;
  shot: Shot;
  sceneId: string;
  generating: boolean;
  dragging: boolean;
  onDragStart: () => void;
  onDragEnd: () => void;
  onDragOverCard: (before: boolean) => void;
  onGenerate: () => void;
}) {
  const [pickingTakes, setPickingTakes] = useState(false);
  const sh = props.shot;
  const empty = sh.takeCount === 0;

  return (
    <div
      className={`board-shot${props.dragging ? " board-dragging" : ""}`}
      draggable
      onDragStart={(e) => {
        e.dataTransfer.effectAllowed = "move";
        props.onDragStart();
      }}
      onDragEnd={props.onDragEnd}
      onDragOver={(e) => {
        e.preventDefault();
        const r = e.currentTarget.getBoundingClientRect();
        props.onDragOverCard(e.clientY < r.top + r.height / 2);
      }}
    >
      {sh.selectedTakeVersionId ? (
        <VersionThumb versionId={sh.selectedTakeVersionId} className="board-thumb" />
      ) : (
        <div className="board-thumb thumb-placeholder-sm">{props.generating ? "⟳" : "▢"}</div>
      )}
      <div className="board-shot-body">
        <div className="name truncate">
          {props.index + 1}. {sh.description || "untitled"}
        </div>
        <div className="meta">
          {sh.durationTargetS > 0 ? `${sh.durationTargetS}s · ` : ""}
          {empty ? "empty" : `${sh.takeCount} take${sh.takeCount > 1 ? "s" : ""}`}
          {sh.selectedTakeId ? " · ✓" : ""}
          {props.generating ? " · ⟳ generating" : ""}
          {sh.continuityStale ? " · ⚠ stale" : ""}
        </div>
        <div className="board-shot-actions">
          <button className="chip-add btn secondary" onClick={props.onGenerate}>
            ⚡ {empty ? "Generate" : "More takes"}
          </button>
          {!empty && (
            <button className="chip-add btn secondary" onClick={() => setPickingTakes(true)}>
              Takes ▾
            </button>
          )}
        </div>
      </div>
      {pickingTakes && (
        <TakePicker
          shotId={sh.id}
          selectedTakeId={sh.selectedTakeId}
          onRegenerate={() => {
            setPickingTakes(false);
            props.onGenerate();
          }}
          onClose={() => setPickingTakes(false)}
        />
      )}
    </div>
  );
}
