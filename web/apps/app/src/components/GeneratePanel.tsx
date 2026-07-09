// Generate panel v1 — the M2 slice of the UX doc's Context-Dock Generate tab
// (02-ui-ux-design.md §3.6): model picker driven by live capability
// manifests, prompt, fan-out count, profile, output bounds, and library
// image references with manifest-declared roles. Targets the project library;
// shot/canvas targeting arrives with M3/M4 surfaces.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { AssetKind, type ModelEndpoint } from "@iris/api-client";
import { assetClient, generationClient, storyClient } from "../api";

type Manifest = {
  modality: "image" | "video";
  tasks: string[];
  profiles: Record<string, { max_width: number; max_height: number }>;
  duration?: { min_s: number; max_s: number };
  references?: { image?: { max: number; roles: string[] } };
  features?: { seed?: boolean; prompt?: boolean };
  pricing?: { unit: string; estimates?: Record<string, number> };
};

type RefChip = { assetId: string; name: string; role: string };

// Prefill for the regenerate-from-this loop (UX doc §3.5: every take exposes
// its recipe as a launchpad). Parsed from a take's recipe JSON.
export type GeneratePrefill = {
  endpointId?: string;
  task?: string;
  profile?: string;
  prompt?: string;
  seed?: bigint;
  durationS?: number;
  refs?: RefChip[];
};

// prefillFromRecipe maps a stored take recipe onto panel state. Unknown or
// missing fields fall back to panel defaults; ref chips carry role labels
// (asset names aren't in the recipe — the id is the identity).
export function prefillFromRecipe(recipeJson: string): GeneratePrefill | undefined {
  try {
    const r = JSON.parse(recipeJson) as {
      endpoint_id?: string;
      task?: string;
      profile?: string;
      request?: {
        prompt?: string;
        output?: { duration_s?: number };
        references?: { kind?: string; role?: string; asset_id?: string }[];
      };
    };
    return {
      endpointId: r.endpoint_id,
      task: r.task,
      profile: r.profile,
      prompt: r.request?.prompt,
      seed: seedFromRecipeJSON(recipeJson),
      durationS: r.request?.output?.duration_s,
      refs: (r.request?.references ?? [])
        .filter((ref) => ref.kind === "image" && ref.asset_id)
        .map((ref) => ({ assetId: ref.asset_id!, name: `${ref.role ?? "ref"}`, role: ref.role ?? "character" })),
    };
  } catch {
    return undefined;
  }
}

// Seeds are int64 in the recipe; JSON.parse coerces numbers to doubles, which
// silently rounds anything above 2^53 — and the seed field itself permits
// 18-digit values. Extract the digits from the raw JSON instead.
export function seedFromRecipeJSON(recipeJson: string): bigint | undefined {
  const m = recipeJson.match(/"seed"\s*:\s*(\d+)/);
  return m ? BigInt(m[1]) : undefined;
}

export function GeneratePanel(props: {
  projectId: string;
  // When set, generated candidates land as this shot's takes.
  target?: { shotId: string; label: string };
  // Regenerate-from-this: initial state from a take's recipe.
  prefill?: GeneratePrefill;
  onClose: () => void;
  onSubmitted: () => void;
}) {
  const qc = useQueryClient();
  const endpoints = useQuery({
    queryKey: ["endpoints"],
    queryFn: () => generationClient.listModelEndpoints({}),
  });
  const healthy = useMemo(
    () =>
      (endpoints.data?.endpoints ?? []).filter((e) => {
        if (!e.healthy || !e.manifestJson) return false;
        // Prompt-ignoring specialists (features.prompt === false, e.g. the
        // LaMa remover) are never offered here — this panel is prompted
        // generation by construction, and every submit would be rejected.
        try {
          return (JSON.parse(e.manifestJson) as Manifest).features?.prompt !== false;
        } catch {
          return false;
        }
      }),
    [endpoints.data],
  );

  const [endpointId, setEndpointId] = useState<string | undefined>(props.prefill?.endpointId);
  const endpoint = healthy.find((e) => e.id === endpointId) ?? healthy[0];
  const manifest = useMemo<Manifest | undefined>(() => {
    if (!endpoint) return undefined;
    try {
      return JSON.parse(endpoint.manifestJson) as Manifest;
    } catch {
      return undefined;
    }
  }, [endpoint]);

  const [task, setTask] = useState<string | undefined>(props.prefill?.task);
  const [profile, setProfile] = useState<string | undefined>(props.prefill?.profile);
  const [prompt, setPrompt] = useState(props.prefill?.prompt ?? "");
  const [count, setCount] = useState(4);
  const [durationS, setDurationS] = useState(props.prefill?.durationS ?? 4);
  const [seed, setSeed] = useState<string>(props.prefill?.seed !== undefined ? String(props.prefill.seed) : "");
  const [refs, setRefs] = useState<RefChip[]>(props.prefill?.refs ?? []);
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
  // Seed: empty = random; otherwise a non-negative integer (only offered
  // when the manifest declares seed support).
  const seedSupported = manifest?.features?.seed === true;
  const seedValue = seed.trim() === "" ? undefined : /^\d{1,18}$/.test(seed.trim()) ? BigInt(seed.trim()) : null;
  const seedValid = !seedSupported || seedValue !== null;
  // Prefilled refs sanitized against the RESOLVED manifest — hidden inputs
  // must never submit (an error about an invisible field is a dead end).
  const effectiveRefs = refDecl
    ? refs.filter((r) => refDecl.roles.includes(r.role)).slice(0, refDecl.max)
    : [];

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
          seed: seedSupported ? (seedValue ?? 0n) : 0n,
          targetEntityId: props.target?.shotId ?? "",
          output: {
            width: profileSpec?.max_width ?? 512,
            height: profileSpec?.max_height ?? 512,
            durationS: isVideo ? durationS : 0,
            fps: isVideo ? 24 : 0,
          },
          references: effectiveRefs.map((r) => ({
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
  if (endpoints.isError) {
    return (
      <aside className="panel">
        <PanelHeader onClose={props.onClose} />
        <div className="status error">Failed to load model endpoints: {String(endpoints.error)}</div>
      </aside>
    );
  }
  if (!endpoint || !manifest) {
    return (
      <aside className="panel">
        <PanelHeader onClose={props.onClose} />
        <div className="empty">No healthy model endpoints. Is the dev stack up?</div>
      </aside>
    );
  }

  // Regenerate fallbacks must be LOUD: silently substituting a model or task
  // while claiming "this take's recipe" would misattribute the spend.
  const fallbackNotes: string[] = [];
  if (props.prefill?.endpointId && endpoint.id !== props.prefill.endpointId) {
    fallbackNotes.push(`Original model unavailable — using ${endpoint.displayName}.`);
  }
  if (props.prefill?.task && activeTask !== props.prefill.task) {
    fallbackNotes.push(`Task "${props.prefill.task}" not supported here — using "${activeTask}".`);
  }
  if (effectiveRefs.length !== refs.length) {
    fallbackNotes.push(`${refs.length - effectiveRefs.length} reference(s) not supported by this model were dropped.`);
  }
  if (!seedSupported && seed.trim() !== "") {
    fallbackNotes.push("This model does not support seeds — generating unseeded.");
  }

  return (
    <aside className="panel">
      <PanelHeader onClose={props.onClose} />
      {props.target && <div className="target-chip">Target: {props.target.label}</div>}
      {fallbackNotes.length > 0 && (
        <div className="status error">{fallbackNotes.join(" ")}</div>
      )}

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
                {r.name === r.role ? r.role : `${r.name} · ${r.role}`}
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
        {seedSupported && (
          <label className="field">
            Seed
            <input
              type="text"
              inputMode="numeric"
              placeholder="random"
              value={seed}
              onChange={(e) => setSeed(e.target.value)}
            />
          </label>
        )}
      </div>

      <button
        className="btn generate"
        disabled={!prompt.trim() || !durationValid || !seedValid || !activeProfile || create.isPending}
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
      {!seedValid && <div className="status error">Seed must be a whole number (or empty for random).</div>}
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
  // Characters resolve to their first image ref (turnaround preferred) —
  // the library-backed consistency loop, pending @-mention polish.
  const characters = useQuery({
    queryKey: ["characters", props.projectId],
    enabled: props.roles.includes("character"),
    queryFn: () => storyClient.listCharacters({ projectId: props.projectId }),
  });

  const characterChip = (name: string, refs: { role: string; asset?: { assetId: string } }[]): RefChip | null => {
    const best =
      refs.find((r) => r.role === "turnaround" && r.asset?.assetId) ??
      refs.find((r) => r.role !== "voice" && r.asset?.assetId);
    return best?.asset ? { assetId: best.asset.assetId, name, role: "character" } : null;
  };

  return (
    <div className="ref-picker">
      {props.roles.includes("character") && (characters.data?.characters.length ?? 0) > 0 && (
        <>
          <span className="field">Characters</span>
          <div className="ref-picker-list">
            {characters.data!.characters.map((c) => {
              const chip = characterChip(c.name, c.refs);
              return (
                <button
                  key={c.id}
                  className="btn secondary"
                  disabled={!chip}
                  title={chip ? undefined : "No image reference yet — add one on the Characters page"}
                  onClick={() => chip && props.onPick(chip)}
                >
                  @{c.name}
                </button>
              );
            })}
          </div>
        </>
      )}
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
