// Generate panel v1 — the M2 slice of the UX doc's Context-Dock Generate tab
// (02-ui-ux-design.md §3.6): model picker driven by live capability
// manifests, prompt, fan-out count, profile, output bounds, and library
// image references with manifest-declared roles. Targets the project library;
// shot/canvas targeting arrives with M3/M4 surfaces.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";
import { AssetKind, type ModelEndpoint } from "@iris/api-client";
import { assetClient, generationClient, storyClient } from "../api";

type Manifest = {
  modality: "image" | "video" | "audio";
  tasks: string[];
  profiles: Record<string, { max_width: number; max_height: number }>;
  duration?: { min_s: number; max_s: number };
  references?: {
    image?: { max: number; roles: string[] };
    audio?: { max: number; roles: string[] };
  };
  params_schema?: {
    properties?: Record<string, { type?: string; enum?: string[]; description?: string }>;
  };
  conditioning?: { first_frame?: boolean; source_video?: boolean };
  features?: { seed?: boolean; prompt?: boolean };
  pricing?: { unit: string; estimates?: Record<string, number> };
};

type RefChip = { assetId: string; name: string; role: string; kind?: "image" | "audio" };

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
  params?: Record<string, string>;
  sourceVideo?: { assetId: string; versionId: string };
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
        params?: Record<string, unknown>;
        conditioning?: { source_video?: { asset_id?: string; version_id?: string } };
      };
    };
    // String params only (voice_id etc.) — replay fidelity: dropping them
    // would silently regenerate with different semantics (default voice).
    const params: Record<string, string> = {};
    for (const [k, v] of Object.entries(r.request?.params ?? {})) {
      if (typeof v === "string") params[k] = v;
    }
    return {
      endpointId: r.endpoint_id,
      task: r.task,
      profile: r.profile,
      prompt: r.request?.prompt,
      seed: seedFromRecipeJSON(recipeJson),
      durationS: r.request?.output?.duration_s,
      // ALL ref kinds replay — filtering to image silently dropped a
      // dialogue take's audio ref from the ♻ Regenerate loop.
      refs: (r.request?.references ?? [])
        .filter((ref) => ref.asset_id)
        .map((ref) => ({
          assetId: ref.asset_id!,
          name: `${ref.role ?? "ref"}`,
          role: ref.role ?? "character",
          kind: ref.kind === "audio" ? ("audio" as const) : ("image" as const),
        })),
      params: Object.keys(params).length > 0 ? params : undefined,
      // Replay fidelity (the PR 31 rule): a lipsync take's source clip must
      // survive ♻ Regenerate or the panel reopens half-configured.
      sourceVideo: r.request?.conditioning?.source_video?.asset_id
        ? {
            assetId: r.request.conditioning.source_video.asset_id,
            versionId: r.request.conditioning.source_video.version_id ?? "",
          }
        : undefined,
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
  const [showAudioPicker, setShowAudioPicker] = useState(false);
  // params_schema-driven fields (enum strings only, v1): voice selection
  // for TTS endpoints is the first consumer.
  const [params, setParams] = useState<Record<string, string>>(props.prefill?.params ?? {});
  // lipsync_post: the clip being re-timed to the audio ref.
  const [sourceVideo, setSourceVideo] = useState<{ assetId: string; name: string; versionId?: string } | null>(
    props.prefill?.sourceVideo ? { ...props.prefill.sourceVideo, name: "source clip" } : null,
  );
  const [showSourcePicker, setShowSourcePicker] = useState(false);
  // Continuity carry (W3): the nearest EARLIER shot in the scene with a
  // selected take supplies its last frame as first_frame conditioning.
  const [carry, setCarry] = useState(true);
  const targetShot = useQuery({
    queryKey: ["shot", props.target?.shotId],
    enabled: !!props.target,
    queryFn: () => storyClient.getShot({ id: props.target!.shotId }),
  });
  const carryScene = useQuery({
    queryKey: ["scene", targetShot.data?.shot?.sceneId],
    enabled: !!targetShot.data?.shot?.sceneId,
    queryFn: () => storyClient.getScene({ id: targetShot.data!.shot!.sceneId }),
  });
  // The carry input: a VIDEO upstream take's last-frame PREP artifact
  // (probed below so the default-on chip can't submit before prep lands —
  // server-side that's a transient retry, but the panel shouldn't fire
  // jobs it knows aren't ready; polls every 3s while missing). An IMAGE
  // take needs no prep — the orchestrator uses it as-is, ready instantly.
  const carrySource = useMemo(() => {
    const me = targetShot.data?.shot;
    const shots = carryScene.data?.scene?.shots;
    if (!me || !shots) return undefined;
    let best: { label: string; versionId: string; isImage: boolean } | undefined;
    for (const [i, sh] of shots.entries()) {
      if (sh.position >= me.position || sh.id === me.id) continue;
      if (sh.selectedTakeVersionId)
        best = {
          label: `Shot ${i + 1}`,
          versionId: sh.selectedTakeVersionId,
          isImage: (sh.selectedTakeContentType ?? "").startsWith("image/"),
        };
    }
    return best;
  }, [targetShot.data, carryScene.data]);
  // W4 voice binding: shot-targeted TTS prefills the voice from the shot's
  // cast (first member with a bound voice that the model's enum accepts).
  // Only fills an UNSET voice — never overrides the user or a recipe.
  const castCharacters = useQuery({
    queryKey: ["characters", props.projectId],
    enabled: !!props.target && (targetShot.data?.shot?.castIds.length ?? 0) > 0,
    queryFn: () => storyClient.listCharacters({ projectId: props.projectId }),
  });
  useEffect(() => {
    // KEY-presence guard, not truthiness: choosing "(default)" sets "",
    // which must stick — SSE shot invalidations re-fire this effect and a
    // truthiness check would flip the user's explicit choice back. params
    // deliberately stays OUT of the deps for the same reason (an eslint-
    // satisfying dep would refill the instant "(default)" is chosen).
    if ("voice_id" in params) return;
    const spec = manifest?.params_schema?.properties?.voice_id;
    if (!spec?.enum) return;
    const cast = targetShot.data?.shot?.castIds ?? [];
    for (const cid of cast) {
      const chr = castCharacters.data?.characters.find((c) => c.id === cid);
      if (chr?.voiceId && spec.enum.includes(chr.voiceId)) {
        setParams((p) => ("voice_id" in p ? p : { ...p, voice_id: chr.voiceId }));
        return;
      }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [castCharacters.data, targetShot.data, manifest]);

  const carryProbe = useQuery({
    queryKey: ["lastFrameReady", carrySource?.versionId],
    enabled: !!carrySource && !carrySource.isImage,
    retry: false,
    refetchInterval: (q) => (q.state.status === "error" ? 3000 : false),
    queryFn: () => assetClient.signDownload({ versionId: carrySource!.versionId, variant: "last_frame" }),
  });
  const carryReady = carrySource?.isImage || carryProbe.isSuccess;

  // lipsync_post is only offerable when the manifest ALSO declares
  // source_video — otherwise the panel would submit a field the user
  // never saw an input for, and the server would reject it.
  const offerableTasks = (manifest?.tasks ?? []).filter(
    (t) => t !== "lipsync_post" || manifest?.conditioning?.source_video === true,
  );
  const activeTask = task && offerableTasks.includes(task) ? task : offerableTasks[0];
  // "draft" is convention, not contract — fall back to the manifest's first
  // declared profile so the select and the submitted value can't diverge.
  const profileKeys = manifest ? Object.keys(manifest.profiles) : [];
  const activeProfile =
    profile && manifest?.profiles[profile] ? profile : profileKeys.includes("draft") ? "draft" : profileKeys[0];
  const profileSpec = activeProfile ? manifest?.profiles[activeProfile] : undefined;
  const isVideo = manifest?.modality === "video";
  const refDecl = manifest?.references?.image;
  const audioDecl = manifest?.references?.audio;
  const isLipsyncPost = activeTask === "lipsync_post";
  const needsSourceVideo = isLipsyncPost && manifest?.conditioning?.source_video === true;
  const enumParams = Object.entries(manifest?.params_schema?.properties ?? {}).filter(
    ([, spec]) => Array.isArray(spec.enum) && spec.enum.length > 0,
  );
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
  const imageRefs = refs.filter((r) => (r.kind ?? "image") === "image");
  const effectiveRefs = refDecl ? imageRefs.filter((r) => refDecl.roles.includes(r.role)).slice(0, refDecl.max) : [];
  const audioRefs = refs.filter((r) => r.kind === "audio");
  const effectiveAudioRefs = audioDecl
    ? audioRefs.filter((r) => audioDecl.roles.includes(r.role)).slice(0, audioDecl.max)
    : [];
  // Same doctrine as refs: hidden inputs never submit — params keys are
  // sanitized against the RESOLVED manifest's schema.
  const paramsJson = (() => {
    const declared = manifest?.params_schema?.properties ?? {};
    const set = Object.fromEntries(Object.entries(params).filter(([k, v]) => v !== "" && k in declared));
    return Object.keys(set).length > 0 ? JSON.stringify(set) : "";
  })();
  // Offered only when it can actually submit: video endpoint that declares
  // first_frame conditioning, with an upstream take to carry from.
  // Carry is meaningless for a re-timing pass — and a first_frame ref in
  // the landed recipe would make the lipsync take a chain member, feeding
  // chain-regen a job shape it can't rebuild.
  const carryActive =
    carry && isVideo && !isLipsyncPost && manifest?.conditioning?.first_frame === true && !!carrySource && carryReady;

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
          // Non-spatial modalities (audio) declare 0-dim profiles: sending
          // ANY output block would fail validation (0×0 is out of bounds,
          // and nonzero exceeds a 0 max) — omit it entirely.
          output:
            (profileSpec?.max_width ?? 0) > 0
              ? {
                  width: profileSpec?.max_width ?? 512,
                  height: profileSpec?.max_height ?? 512,
                  durationS: isVideo ? durationS : 0,
                  fps: isVideo ? 24 : 0,
                }
              : undefined,
          references: [...effectiveRefs, ...effectiveAudioRefs].map((r) => ({
            kind: r.kind ?? "image",
            role: r.role,
            asset: { assetId: r.assetId, versionId: "" },
          })),
          paramsJson,
          // A VIDEO version as first_frame means "its last frame" (the prep
          // artifact) — the orchestrator resolves the derived key.
          conditioning:
            carryActive || (needsSourceVideo && sourceVideo)
              ? {
                  firstFrame: carryActive ? { assetId: "", versionId: carrySource!.versionId } : undefined,
                  sourceVideo:
                    needsSourceVideo && sourceVideo
                      ? { assetId: sourceVideo.assetId, versionId: sourceVideo.versionId ?? "" }
                      : undefined,
                }
              : undefined,
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
  if (effectiveRefs.length + effectiveAudioRefs.length !== refs.length) {
    fallbackNotes.push(
      `${refs.length - effectiveRefs.length - effectiveAudioRefs.length} reference(s) not supported by this model were dropped.`,
    );
  }
  if (!seedSupported && seed.trim() !== "") {
    fallbackNotes.push("This model does not support seeds — generating unseeded.");
  }

  return (
    <aside className="panel">
      <PanelHeader onClose={props.onClose} />
      {props.target && <div className="target-chip">Target: {props.target.label}</div>}
      {props.target && isVideo && !isLipsyncPost && manifest.conditioning?.first_frame === true && carrySource && (
        <label className="carry-chip" title={carrySource.isImage ? "Sends the upstream take (a still) as first_frame conditioning" : "Sends the upstream take's last frame as first_frame conditioning"}>
          <input type="checkbox" checked={carry && carryReady} disabled={!carryReady} onChange={(e) => setCarry(e.target.checked)} />
          ⛓ Continue from {carrySource.label}’s {carrySource.isImage ? "frame" : "last frame"}
          {!carryReady && <span className="meta"> (preparing frame…)</span>}
        </label>
      )}
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
            setShowAudioPicker(false);
            setParams({}); // params_schema is per-model
            setSourceVideo(null);

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
          {offerableTasks.map((t) => (
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

      {enumParams.length > 0 && (
        <div className="field-row">
          {enumParams.map(([name, spec]) => (
            <label key={name} className="field" title={spec.description}>
              {name.replace(/_/g, " ")}
              <select
                value={params[name] ?? ""}
                onChange={(e) => setParams({ ...params, [name]: e.target.value })}
              >
                <option value="">(default)</option>
                {spec.enum!.map((v) => (
                  <option key={v} value={v}>
                    {v}
                  </option>
                ))}
              </select>
            </label>
          ))}
        </div>
      )}

      {refDecl && (
        <div className="field">
          <span>
            References ({imageRefs.length}/{refDecl.max})
          </span>
          <div className="chips">
            {imageRefs.map((r) => (
              <span key={`${r.assetId}:${r.role}`} className="chip" title={r.role}>
                {r.name === r.role ? r.role : `${r.name} · ${r.role}`}
                <button onClick={() => setRefs(refs.filter((x) => x !== r))}>×</button>
              </span>
            ))}
            {imageRefs.length < refDecl.max && (
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

      {needsSourceVideo && (
        <div className="field">
          <span>Source clip (re-timed to the audio)</span>
          <div className="chips">
            {sourceVideo ? (
              <span className="chip">
                🎬 {sourceVideo.name}
                <button onClick={() => setSourceVideo(null)}>×</button>
              </span>
            ) : (
              <button className="btn secondary chip-add" onClick={() => setShowSourcePicker(true)}>
                + Pick video
              </button>
            )}
          </div>
          {showSourcePicker && (
            <VideoSourcePicker
              projectId={props.projectId}
              onPick={(v) => {
                setSourceVideo(v);
                setShowSourcePicker(false);
              }}
              onClose={() => setShowSourcePicker(false)}
            />
          )}
        </div>
      )}

      {audioDecl && (
        <div className="field">
          <span>
            Audio ({audioRefs.length}/{audioDecl.max}) — {audioDecl.roles.join("/")}
          </span>
          <div className="chips">
            {audioRefs.map((r) => (
              <span key={r.assetId} className="chip" title={r.role}>
                🎵 {r.name}
                <button onClick={() => setRefs(refs.filter((x) => x !== r))}>×</button>
              </span>
            ))}
            {audioRefs.length < audioDecl.max && (
              <button className="btn secondary chip-add" onClick={() => setShowAudioPicker(true)}>
                + Add audio
              </button>
            )}
          </div>
          {showAudioPicker && (
            <AudioRefPicker
              projectId={props.projectId}
              role={audioDecl.roles[0]}
              onPick={(chip) => {
                setRefs([...refs, chip]);
                setShowAudioPicker(false);
              }}
              onClose={() => setShowAudioPicker(false)}
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
        disabled={
          (!prompt.trim() && !isLipsyncPost) ||
          (needsSourceVideo && !sourceVideo) ||
          !durationValid ||
          !seedValid ||
          !activeProfile ||
          create.isPending
        }
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

/** Source-clip picker for lipsync_post — library video only. */
function VideoSourcePicker(props: {
  projectId: string;
  onPick: (v: { assetId: string; name: string; versionId?: string }) => void;
  onClose: () => void;
}) {
  const assets = useQuery({
    queryKey: ["assets", props.projectId, "video-source-picker"],
    queryFn: () => assetClient.listAssets({ projectId: props.projectId, kind: AssetKind.VIDEO }),
  });
  return (
    <div className="ref-picker">
      <span className="field">Library video</span>
      {(assets.data?.assets.length ?? 0) === 0 && <div className="empty">No video in the library yet.</div>}
      <div className="ref-picker-list">
        {assets.data?.assets.map((a) => (
          <button
            key={a.id}
            className="btn secondary"
            // Pin the head the user actually saw: an assetId-only ref floats
            // on later head moves AND loses its lineage edge outside the
            // dev-cache pinning pass.
            onClick={() => props.onPick({ assetId: a.id, name: a.name, versionId: a.headVersionId })}
          >
            🎬 <span className="truncate">{a.name}</span>
          </button>
        ))}
      </div>
      <button className="btn secondary" onClick={props.onClose}>
        Cancel
      </button>
    </div>
  );
}

/** Audio reference picker — library audio (TTS output, uploads) for
 * lip-sync/audio-conditioned generation. */
function AudioRefPicker(props: {
  projectId: string;
  role: string;
  onPick: (c: RefChip) => void;
  onClose: () => void;
}) {
  const assets = useQuery({
    queryKey: ["assets", props.projectId, "audio-ref-picker"],
    queryFn: () => assetClient.listAssets({ projectId: props.projectId, kind: AssetKind.AUDIO }),
  });
  return (
    <div className="ref-picker">
      <span className="field">Library audio</span>
      {(assets.data?.assets.length ?? 0) === 0 && (
        <div className="empty">No audio in the library yet — generate a voice line first.</div>
      )}
      <div className="ref-picker-list">
        {assets.data?.assets.map((a) => (
          <button
            key={a.id}
            className="btn secondary"
            onClick={() => props.onPick({ assetId: a.id, name: a.name, role: props.role, kind: "audio" })}
          >
            🎵 <span className="truncate">{a.name}</span>
          </button>
        ))}
      </div>
      <button className="btn secondary" onClick={props.onClose}>
        Cancel
      </button>
    </div>
  );
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
