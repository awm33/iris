import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { assetClient } from "../api";
import { type GenFillEndpoint, pickRemovalEndpoint } from "./genfill";

export type GenFillState =
  | { phase: "submitting" }
  | { phase: "generating"; jobId: string; maskVersionId: string; maskAssetId: string; removal?: boolean }
  | {
      phase: "choosing";
      jobId: string;
      maskVersionId: string;
      maskAssetId: string;
      candidates: string[]; // artifact version ids
      index: number;
      removal?: boolean;
    };

/**
 * The gen-fill loop's inline UI: prompt bar while a selection is armed,
 * progress while generating, and the candidate strip (arrow through
 * in-place previews, commit as a masked layer) when results land.
 */
export function GenFillBar(props: {
  endpoints: GenFillEndpoint[];
  /** Canvas dims — removal auto-routing must pick an endpoint that fits. */
  docW: number;
  docH: number;
  state?: GenFillState;
  progress?: number;
  error?: string;
  onGenerate: (prompt: string, count: number, endpoint: GenFillEndpoint) => void;
  onPick: (index: number) => void;
  onCommit: () => void;
  onDiscard: () => void;
}) {
  const [prompt, setPrompt] = useState("");
  const [count, setCount] = useState(4);
  const [endpointId, setEndpointId] = useState<string>();
  const st = props.state;
  // Prompted generation only offers endpoints that condition on prompts;
  // Remove auto-routes (specialist first — the fast tier).
  const promptable = props.endpoints.filter((e) => e.promptable);
  const endpoint = promptable.find((e) => e.id === endpointId) ?? promptable[0];
  const removalEndpoint = pickRemovalEndpoint(props.endpoints, props.docW, props.docH);

  if (st?.phase === "choosing") {
    return (
      <div className="genfill-bar">
        <span className="meta">
          {st.candidates.length > 1
            ? `Candidate ${st.index + 1}/${st.candidates.length} — ←/→ to compare, Enter to commit`
            : `${st.removal ? "Removal" : "Result"} — Enter to commit`}
        </span>
        <div className="genfill-strip">
          {st.candidates.map((v, i) => (
            <CandidateThumb key={v} versionId={v} selected={i === st.index} onClick={() => props.onPick(i)} />
          ))}
        </div>
        <button className="btn" onClick={props.onCommit}>
          ✓ Commit as layer
        </button>
        <button className="btn secondary" onClick={props.onDiscard}>
          Discard
        </button>
      </div>
    );
  }

  if (st?.phase === "generating" || st?.phase === "submitting") {
    return (
      <div className="genfill-bar">
        <span className="meta">
          {st.phase === "submitting"
            ? "Uploading source + mask…"
            : `Generating… ${Math.round((props.progress ?? 0) * 100)}%`}
        </span>
        {st.phase === "generating" && (
          <button className="btn secondary" onClick={props.onDiscard}>
            Cancel
          </button>
        )}
      </div>
    );
  }

  if (props.endpoints.length === 0) {
    return (
      <div className="genfill-bar">
        <span className="status error">No endpoint offers gen-fill (task "inpaint" + mask/source_image).</span>
      </div>
    );
  }

  return (
    <div className="genfill-bar">
      <input
        type="text"
        placeholder="Generate into selection… (prompt)"
        value={prompt}
        autoFocus
        onChange={(e) => setPrompt(e.target.value)}
        onKeyDown={(e) =>
          e.key === "Enter" && prompt.trim() && endpoint && props.onGenerate(prompt.trim(), count, endpoint)
        }
        style={{ flex: 1 }}
      />
      {promptable.length > 1 && (
        <select value={endpoint?.id} onChange={(e) => setEndpointId(e.target.value)} aria-label="Model">
          {promptable.map((e) => (
            <option key={e.id} value={e.id}>
              {e.name}
            </option>
          ))}
        </select>
      )}
      <select value={count} onChange={(e) => setCount(Number(e.target.value))} aria-label="Candidates">
        {[1, 2, 4, 6].map((n) => (
          <option key={n} value={n}>
            ×{n}
          </option>
        ))}
      </select>
      <button
        className="btn"
        disabled={!prompt.trim() || !endpoint}
        onClick={() => endpoint && props.onGenerate(prompt.trim(), count, endpoint)}
      >
        ⚡ Generate
      </button>
      <button
        className="btn secondary"
        disabled={!removalEndpoint}
        title={
          removalEndpoint
            ? `Remove: reconstruct the background under the selection (${removalEndpoint.name})`
            : "No endpoint fits this canvas for removal"
        }
        onClick={() => removalEndpoint && props.onGenerate("", 1, removalEndpoint)}
      >
        ✂ Remove
      </button>
      {props.error && <span className="status error">{props.error}</span>}
    </div>
  );
}

function CandidateThumb(props: { versionId: string; selected: boolean; onClick: () => void }) {
  const thumb = useQuery({
    queryKey: ["thumb", props.versionId, ""],
    staleTime: 10 * 60 * 1000,
    queryFn: () => assetClient.signDownload({ versionId: props.versionId }),
  });
  return (
    <button className={`genfill-thumb${props.selected ? " selected" : ""}`} onClick={props.onClick}>
      {thumb.data ? <img src={thumb.data.url} alt="candidate" /> : <span className="meta">…</span>}
    </button>
  );
}
