import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { storyClient } from "../api";
import { useEscape, VersionThumb } from "./AssetThumb";

// Take Picker v1 (UX doc §3.5): grid of a shot's candidates, selected state,
// one-click re-selection. Synced playback/A-B compare arrive with the video
// studio (M5).
export function TakePicker(props: { shotId: string; selectedTakeId: string; onClose: () => void }) {
  useEscape(props.onClose);
  const qc = useQueryClient();
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

  return (
    <div className="overlay" onClick={props.onClose}>
      <div className="modal" role="dialog" aria-modal="true" onClick={(e) => e.stopPropagation()}>
        <div className="panel-header">
          <h3>Takes</h3>
          <button className="btn secondary" onClick={props.onClose}>
            Close
          </button>
        </div>
        {takes.isLoading && <div className="empty">Loading…</div>}
        {takes.isError && <div className="status error">Failed to load takes: {String(takes.error)}</div>}
        {takes.data?.takes.length === 0 && (
          <div className="empty">No takes yet — generate some into this shot.</div>
        )}
        <div className="picker-grid">
          {takes.data?.takes.map((t, i) => {
            const isSelected = t.id === props.selectedTakeId;
            return (
              <button
                key={t.id}
                className={`picker-cell take-cell ${isSelected ? "selected" : ""}`}
                disabled={select.isPending}
                onClick={() => !isSelected && select.mutate(t.id)}
                title={recipeSummary(t.recipeJson)}
              >
                <VersionThumb versionId={t.versionId} className="picker-thumb" />
                <span className="picker-name">
                  Take {i + 1} · {t.quality}
                  {isSelected ? " ✓" : ""}
                </span>
              </button>
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
