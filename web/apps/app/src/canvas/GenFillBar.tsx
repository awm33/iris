import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { assetClient } from "../api";
import type { GenFillEndpoint } from "./genfill";

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
  const endpoint = props.endpoints.find((e) => e.id === endpointId) ?? props.endpoints[0];

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
        onKeyDown={(e) => e.key === "Enter" && prompt.trim() && props.onGenerate(prompt.trim(), count, endpoint)}
        style={{ flex: 1 }}
      />
      {props.endpoints.length > 1 && (
        <select value={endpoint.id} onChange={(e) => setEndpointId(e.target.value)} aria-label="Model">
          {props.endpoints.map((e) => (
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
      <button className="btn" disabled={!prompt.trim()} onClick={() => props.onGenerate(prompt.trim(), count, endpoint)}>
        ⚡ Generate
      </button>
      <button
        className="btn secondary"
        title="Remove: reconstruct the background under the selection (no prompt)"
        onClick={() => props.onGenerate("", 1, endpoint)}
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
