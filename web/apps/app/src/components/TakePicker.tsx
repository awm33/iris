import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { storyClient } from "../api";
import { useEscape, VersionThumb } from "./AssetThumb";

// Take Picker (UX doc §3.5): grid of a shot's candidates, selected state,
// one-click re-selection, per-take regenerate-from-this, and the keyboard
// contract — digits select take N, arrows move the highlight, Enter commits
// the highlight, Escape closes. Synced playback/A-B compare arrive with the
// video studio (M5).
export function TakePicker(props: {
  shotId: string;
  selectedTakeId: string;
  onRegenerate: (recipeJson: string) => void;
  onClose: () => void;
}) {
  useEscape(props.onClose);
  const qc = useQueryClient();
  const [highlight, setHighlight] = useState(0);
  const takes = useQuery({
    queryKey: ["takes", props.shotId],
    queryFn: () => storyClient.listTakes({ shotId: props.shotId }),
  });
  const select = useMutation({
    mutationFn: (takeId: string) => storyClient.selectTake({ shotId: props.shotId, takeId }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["scene"] });
      void qc.invalidateQueries({ queryKey: ["takes", props.shotId] });
    },
  });

  const list = takes.data?.takes ?? [];

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (list.length === 0 || select.isPending) return;
      if (e.key >= "1" && e.key <= "9") {
        const i = Number(e.key) - 1;
        if (i < list.length && list[i].id !== props.selectedTakeId) {
          setHighlight(i);
          select.mutate(list[i].id);
        }
      } else if (e.key === "ArrowRight" || e.key === "ArrowDown") {
        e.preventDefault();
        setHighlight((h) => Math.min(h + 1, list.length - 1));
      } else if (e.key === "ArrowLeft" || e.key === "ArrowUp") {
        e.preventDefault();
        setHighlight((h) => Math.max(h - 1, 0));
      } else if (e.key === "Enter") {
        const t = list[highlight];
        if (t && t.id !== props.selectedTakeId) select.mutate(t.id);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [list, highlight, select, props.selectedTakeId]);

  return (
    <div className="overlay" onClick={props.onClose}>
      <div className="modal" role="dialog" aria-modal="true" onClick={(e) => e.stopPropagation()}>
        <div className="panel-header">
          <h3>Takes</h3>
          <span className="meta">1–9 select · ←→ highlight · ↵ commit · esc close</span>
          <button className="btn secondary" onClick={props.onClose}>
            Close
          </button>
        </div>
        {takes.isLoading && <div className="empty">Loading…</div>}
        {takes.isError && <div className="status error">Failed to load takes: {String(takes.error)}</div>}
        {list.length === 0 && !takes.isLoading && (
          <div className="empty">No takes yet — generate some into this shot.</div>
        )}
        <div className="picker-grid">
          {list.map((t, i) => {
            const isSelected = t.id === props.selectedTakeId;
            return (
              <div
                key={t.id}
                className={`picker-cell take-cell ${isSelected ? "selected" : ""} ${i === highlight ? "highlight" : ""}`}
              >
                <button
                  className="take-select"
                  disabled={select.isPending}
                  title={recipeSummary(t.recipeJson)}
                  onClick={() => {
                    setHighlight(i);
                    if (!isSelected) select.mutate(t.id);
                  }}
                >
                  <VersionThumb versionId={t.versionId} className="picker-thumb" />
                  <span className="picker-name">
                    Take {i + 1} · {t.quality}
                    {isSelected ? " ✓" : ""}
                  </span>
                </button>
                <button
                  className="btn secondary chip-add"
                  title="Open the generate panel pre-filled with this take's exact recipe"
                  onClick={() => props.onRegenerate(t.recipeJson)}
                >
                  ♻ Regenerate from this
                </button>
              </div>
            );
          })}
        </div>
        {select.isError && <div className="status error">{String(select.error)}</div>}
      </div>
    </div>
  );
}

function recipeSummary(recipeJson: string): string {
  try {
    const r = JSON.parse(recipeJson) as { model?: string; request?: { prompt?: string; seed?: number } };
    return [r.model, r.request?.seed !== undefined ? `seed ${r.request.seed}` : "", r.request?.prompt ?? ""]
      .filter(Boolean)
      .join(" · ");
  } catch {
    return "";
  }
}
