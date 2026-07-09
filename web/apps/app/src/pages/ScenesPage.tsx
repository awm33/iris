import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { storyClient } from "../api";

export function ScenesPage(props: { projectId: string; onOpen: (sceneId: string) => void }) {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const scenes = useQuery({
    queryKey: ["scenes", props.projectId],
    queryFn: () => storyClient.listScenes({ projectId: props.projectId }),
  });
  const create = useMutation({
    mutationFn: (n: string) => storyClient.createScene({ projectId: props.projectId, name: n }),
    onSuccess: (res) => {
      setName("");
      void qc.invalidateQueries({ queryKey: ["scenes"] });
      if (res.scene) props.onOpen(res.scene.id);
    },
  });

  const submit = () => {
    if (name.trim() && !create.isPending) create.mutate(name.trim());
  };

  return (
    <div>
      <h2>Scenes</h2>
      <div className="toolbar">
        <input
          type="text"
          placeholder="New scene name… (e.g. Diner)"
          value={name}
          onChange={(e) => setName(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && submit()}
        />
        <button className="btn" disabled={!name.trim() || create.isPending} onClick={submit}>
          Create scene
        </button>
      </div>
      {create.isError && <div className="status error">{String(create.error)}</div>}
      {scenes.isLoading && <div className="empty">Loading…</div>}
      {scenes.isError && <div className="status error">Failed to load scenes: {String(scenes.error)}</div>}
      {scenes.data?.scenes.length === 0 && (
        <div className="empty">
          No scenes yet. A scene is a place in your story — "the diner", "rooftop at night". Create one, catalog
          reference views of its set, then shoot it.
        </div>
      )}
      <div className="grid">
        {scenes.data?.scenes.map((s) => (
          <button key={s.id} className="card card-button" onClick={() => props.onOpen(s.id)}>
            <span className="name truncate">{s.name}</span>
            <span className="meta truncate">{s.styleNotes || "no style notes"}</span>
          </button>
        ))}
      </div>
    </div>
  );
}
