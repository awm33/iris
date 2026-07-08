// Generate panel v1 — the M2 slice of the UX doc's Context-Dock Generate tab
// (02-ui-ux-design.md §3.6): model picker driven by live capability
// manifests, prompt, fan-out count, profile, output bounds, and library
// image references with manifest-declared roles. Targets the project library;
// shot/canvas targeting arrives with M3/M4 surfaces.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { AssetKind, type ModelEndpoint } from "@iris/api-client";
import { assetClient, generationClient } from "../api";

type Manifest = {
  modality: "image" | "video";
  tasks: string[];
  profiles: Record<string, { max_width: number; max_height: number }>;
  duration?: { min_s: number; max_s: number };
  references?: { image?: { max: number; roles: string[] } };
  pricing?: { unit: string; estimates?: Record<string, number> };
};

type RefChip = { assetId: string; name: string; role: string };

export function GeneratePanel(props: { projectId: string; onClose: () => void; onSubmitted: () => void }) {
  const qc = useQueryClient();
  const endpoints = useQuery({
    queryKey: ["endpoints"],
    queryFn: () => generationClient.listModelEndpoints({}),
  });
  const healthy = useMemo(
    () => (endpoints.data?.endpoints ?? []).filter((e) => e.healthy && e.manifestJson),
    [endpoints.data],
  );

  const [endpointId, setEndpointId] = useState<string>();
  const endpoint = healthy.find((e) => e.id === endpointId) ?? healthy[0];
  const manifest = useMemo<Manifest | undefined>(() => {
    if (!endpoint) return undefined;
    try {
      return JSON.parse(endpoint.manifestJson) as Manifest;
    } catch {
      return undefined;
    }
  }, [endpoint]);

  const [task, setTask] = useState<string>();
  const [profile, setProfile] = useState<string>();
  const [prompt, setPrompt] = useState("");
  const [count, setCount] = useState(4);
  const [durationS, setDurationS] = useState(4);
  const [refs, setRefs] = useState<RefChip[]>([]);
  const [showRefPicker, setShowRefPicker] = useState(false);

  const activeTask = task && manifest?.tasks.includes(task) ? task : manifest?.tasks[0];
  const activeProfile = profile && manifest?.profiles[profile] ? profile : "draft";
  const profileSpec = manifest?.profiles[activeProfile];
  const isVideo = manifest?.modality === "video";
  const refDecl = manifest?.references?.image;
  const estimate = manifest?.pricing?.estimates?.[activeProfile];

  const create = useMutation({
    mutationFn: () =>
      generationClient.createJob({
        job: {
          projectId: props.projectId,
          modelEndpointId: endpoint!.id,
          task: activeTask!,
          profile: activeProfile,
          prompt,
          count,
          output: {
            width: profileSpec?.max_width ?? 512,
            height: profileSpec?.max_height ?? 512,
            durationS: isVideo ? durationS : 0,
            fps: isVideo ? 24 : 0,
          },
          references: refs.map((r) => ({
            kind: "image",
            role: r.role,
            asset: { assetId: r.assetId, versionId: "" },
          })),
        },
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["jobs"] });
      props.onSubmitted();
    },
  });

  if (endpoints.isLoading) return <aside className="panel">Loading models…</aside>;
  if (!endpoint || !manifest) {
    return (
      <aside className="panel">
        <PanelHeader onClose={props.onClose} />
        <div className="empty">No healthy model endpoints. Is the dev stack up?</div>
      </aside>
    );
  }

  return (
    <aside className="panel">
      <PanelHeader onClose={props.onClose} />

      <label className="field">
        Model
        <select value={endpoint.id} onChange={(e) => setEndpointId(e.target.value)}>
          {healthy.map((ep) => (
            <option key={ep.id} value={ep.id}>
              {ep.displayName} · {modalityOf(ep)}
            </option>
          ))}
        </select>
      </label>

      <label className="field">
        Task
        <select value={activeTask} onChange={(e) => setTask(e.target.value)}>
          {manifest.tasks.map((t) => (
            <option key={t}>{t}</option>
          ))}
        </select>
      </label>

      <label className="field">
        Prompt
        <textarea
          rows={4}
          placeholder="Describe the shot…"
          value={prompt}
          onChange={(e) => setPrompt(e.target.value)}
        />
      </label>

      {refDecl && (
        <div className="field">
          <span>
            References ({refs.length}/{refDecl.max})
          </span>
          <div className="chips">
            {refs.map((r, i) => (
              <span key={i} className="chip" title={r.role}>
                {r.name} · {r.role}
                <button onClick={() => setRefs(refs.filter((_, j) => j !== i))}>×</button>
              </span>
            ))}
            {refs.length < refDecl.max && (
              <button className="btn secondary chip-add" onClick={() => setShowRefPicker(true)}>
                + Add reference
              </button>
            )}
          </div>
          {showRefPicker && (
            <RefPicker
              projectId={props.projectId}
              roles={refDecl.roles}
              onPick={(chip) => {
                setRefs([...refs, chip]);
                setShowRefPicker(false);
              }}
              onClose={() => setShowRefPicker(false)}
            />
          )}
        </div>
      )}

      <div className="field-row">
        <label className="field">
          Takes
          <select value={count} onChange={(e) => setCount(Number(e.target.value))}>
            {[1, 2, 4, 8].map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </select>
        </label>
        <label className="field">
          Quality
          <select value={activeProfile} onChange={(e) => setProfile(e.target.value)}>
            {Object.keys(manifest.profiles).map((p) => (
              <option key={p}>{p}</option>
            ))}
          </select>
        </label>
        {isVideo && manifest.duration && (
          <label className="field">
            Seconds
            <input
              type="number"
              min={manifest.duration.min_s}
              max={manifest.duration.max_s}
              value={durationS}
              onChange={(e) => setDurationS(Number(e.target.value))}
            />
          </label>
        )}
      </div>

      <button
        className="btn generate"
        disabled={!prompt.trim() || create.isPending}
        onClick={() => create.mutate()}
      >
        ⚡ Generate {count} {isVideo ? "take" : "candidate"}
        {count > 1 ? "s" : ""}
        {estimate !== undefined ? ` · ~${(estimate * count).toFixed(1)} ${manifest.pricing?.unit === "gpu_second" ? "gpu·s" : ""}` : ""}
      </button>
      {create.isError && <div className="status error">{String(create.error)}</div>}
    </aside>
  );
}

function PanelHeader(props: { onClose: () => void }) {
  return (
    <div className="panel-header">
      <h3>Generate</h3>
      <button className="btn secondary" onClick={props.onClose}>
        Close
      </button>
    </div>
  );
}

function modalityOf(ep: ModelEndpoint): string {
  try {
    return (JSON.parse(ep.manifestJson) as Manifest).modality;
  } catch {
    return "?";
  }
}

function RefPicker(props: {
  projectId: string;
  roles: string[];
  onPick: (c: RefChip) => void;
  onClose: () => void;
}) {
  const [role, setRole] = useState(props.roles[0]);
  const assets = useQuery({
    queryKey: ["assets", props.projectId, "ref-picker"],
    queryFn: () => assetClient.listAssets({ projectId: props.projectId, kind: AssetKind.IMAGE }),
  });
  return (
    <div className="ref-picker">
      <label className="field">
        Role
        <select value={role} onChange={(e) => setRole(e.target.value)}>
          {props.roles.map((r) => (
            <option key={r}>{r}</option>
          ))}
        </select>
      </label>
      <div className="ref-picker-list">
        {assets.data?.assets.length === 0 && <div className="empty">No images in this project yet.</div>}
        {assets.data?.assets.map((a) => (
          <button
            key={a.id}
            className="btn secondary"
            onClick={() => props.onPick({ assetId: a.id, name: a.name, role })}
          >
            {a.name}
          </button>
        ))}
      </div>
      <button className="btn secondary" onClick={props.onClose}>
        Cancel
      </button>
    </div>
  );
}
