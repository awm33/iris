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

  return (
    <div>
      <h2>Scenes</h2>
      <div className="toolbar">
        <input
          type="text"
          placeholder="New scene name… (e.g. Diner)"
          value={name}
          onChange={(e) => setName(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && name.trim() && create.mutate(name.trim())}
        />
        <button className="btn" disabled={!name.trim() || create.isPending} onClick={() => create.mutate(name.trim())}>
          Create scene
        </button>
      </div>
      {scenes.data?.scenes.length === 0 && (
        <div className="empty">
          No scenes yet. A scene is a place in your story — "the diner", "rooftop at night". Create one, catalog
          reference views of its set, then shoot it.
        </div>
      )}
      <div className="grid">
        {scenes.data?.scenes.map((s) => (
          <div key={s.id} className="card" onClick={() => props.onOpen(s.id)}>
            <div className="name">{s.name}</div>
            <div className="meta">{s.styleNotes || "no style notes"}</div>
          </div>
        ))}
      </div>
    </div>
  );
}
