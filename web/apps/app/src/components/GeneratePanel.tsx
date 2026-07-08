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
  // "draft" is convention, not contract — fall back to the manifest's first
  // declared profile so the select and the submitted value can't diverge.
  const profileKeys = manifest ? Object.keys(manifest.profiles) : [];
  const activeProfile =
    profile && manifest?.profiles[profile] ? profile : profileKeys.includes("draft") ? "draft" : profileKeys[0];
  const profileSpec = activeProfile ? manifest?.profiles[activeProfile] : undefined;
  const isVideo = manifest?.modality === "video";
  const refDecl = manifest?.references?.image;
  const estimate = activeProfile ? manifest?.pricing?.estimates?.[activeProfile] : undefined;

  // Duration clamped to the manifest's declared range; an empty number input
  // (NaN/0) must never submit — duration=0 would bypass video validation.
  const durMin = manifest?.duration?.min_s ?? 1;
  const durMax = manifest?.duration?.max_s ?? 60;
  const durationValid = !isVideo || (Number.isFinite(durationS) && durationS >= durMin && durationS <= durMax);

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
        <select
          value={endpoint.id}
          onChange={(e) => {
            // References are validated against the selected model's manifest;
            // carrying them across a model switch would submit chips the
            // panel no longer displays.
            setEndpointId(e.target.value);
            setRefs([]);
            setShowRefPicker(false);
          }}
        >
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
        disabled={!prompt.trim() || !durationValid || !activeProfile || create.isPending}
        onClick={() => create.mutate()}
      >
        ⚡ Generate {count} {isVideo ? "take" : "candidate"}
        {count > 1 ? "s" : ""}
        {formatEstimate(estimate, count, manifest.pricing?.unit)}
      </button>
      {!durationValid && (
        <div className="status error">
          Duration must be between {durMin} and {durMax} seconds.
        </div>
      )}
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

const unitLabels: Record<string, string> = {
  gpu_second: "gpu·s",
  usd_per_second: "USD",
  usd_per_image: "USD",
  usd_per_job: "USD",
};

function formatEstimate(estimate: number | undefined, count: number, unit?: string): string {
  if (estimate === undefined || !Number.isFinite(estimate)) return "";
  const total = estimate * count;
  if (!Number.isFinite(total) || total < 0) return "";
  return ` · ~${total.toFixed(1)} ${unitLabels[unit ?? ""] ?? unit ?? ""}`.trimEnd();
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
