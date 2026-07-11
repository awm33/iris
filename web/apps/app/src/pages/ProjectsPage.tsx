import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { workspaceClient } from "../api";

export function ProjectsPage(props: { onOpen: (projectId: string, name: string) => void }) {
  const qc = useQueryClient();
  const [name, setName] = useState("");

  const projects = useQuery({
    queryKey: ["projects"],
    queryFn: () => workspaceClient.listProjects({}),
  });

  const create = useMutation({
    mutationFn: (n: string) => workspaceClient.createProject({ name: n }),
    onSuccess: () => {
      setName("");
      void qc.invalidateQueries({ queryKey: ["projects"] });
    },
  });

  return (
    <div>
      <h2>Projects</h2>
      <div className="toolbar">
        <input
          type="text"
          placeholder="New project name…"
          value={name}
          onChange={(e) => setName(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && name.trim() && create.mutate(name.trim())}
        />
        <button className="btn" disabled={!name.trim() || create.isPending} onClick={() => create.mutate(name.trim())}>
          Create project
        </button>
        {create.isError && <span className="status">create failed: {String(create.error)}</span>}
      </div>

      {projects.isLoading && <div className="empty">Loading…</div>}
      {projects.isError && <div className="empty">Failed to load projects: {String(projects.error)}</div>}
      {projects.data && projects.data.projects.length === 0 && (
        <div className="empty">No projects yet — create the first one above.</div>
      )}
      <div className="grid">
        {projects.data?.projects.map((p) => (
          <button key={p.id} className="card card-button" onClick={() => props.onOpen(p.id, p.name)}>
            <div className="name">{p.name}</div>
            <div className="meta">{p.description || p.id}</div>
          </button>
        ))}
      </div>
    </div>
  );
}
