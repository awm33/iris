import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { storyClient } from "../api";
import { useEscape, VersionThumb } from "./AssetThumb";
import { ClipPlayer } from "./ClipPlayer";

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
  const qc = useQueryClient();
  const [highlight, setHighlight] = useState(0);
  const [playingVersion, setPlayingVersion] = useState<string>();
  // While a take is playing, the player owns the keyboard (incl. Esc).
  useEscape(playingVersion ? () => setPlayingVersion(undefined) : props.onClose);
  const takes = useQuery({
    queryKey: ["takes", props.shotId],
    queryFn: () => storyClient.listTakes({ shotId: props.shotId }),
  });
  const select = useMutation({
    mutationFn: (takeId: string) => storyClient.selectTake({ shotId: props.shotId, takeId }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["scene"] });
      void qc.invalidateQueries({ queryKey: ["takes", props.shotId] });
      void qc.invalidateQueries({ queryKey: ["shot", props.shotId] }); // timeline preview resolution
    },
  });

  const list = takes.data?.takes ?? [];

  // Open on the currently selected take; clamp when the list shrinks.
  useEffect(() => {
    const i = list.findIndex((t) => t.id === props.selectedTakeId);
    setHighlight((h) => (i >= 0 && h === 0 ? i : Math.min(h, Math.max(list.length - 1, 0))));
  }, [list, props.selectedTakeId]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (playingVersion) return; // player owns the keyboard
      if (list.length === 0 || select.isPending) return;
      // Never hijack modified combos or typing contexts (focus is not
      // trapped; background inputs remain tabbable).
      if (e.metaKey || e.ctrlKey || e.altKey) return;
      const target = e.target as HTMLElement | null;
      if (
        target instanceof HTMLInputElement ||
        target instanceof HTMLTextAreaElement ||
        target?.isContentEditable
      ) {
        return;
      }
      if (e.key >= "1" && e.key <= "9") {
        const i = Number(e.key) - 1;
        if (i < list.length) {
          setHighlight(i);
          if (list[i].id !== props.selectedTakeId) select.mutate(list[i].id);
        }
      } else if (e.key === "ArrowRight" || e.key === "ArrowDown") {
        e.preventDefault();
        setHighlight((h) => Math.min(h + 1, list.length - 1));
      } else if (e.key === "ArrowLeft" || e.key === "ArrowUp") {
        e.preventDefault();
        setHighlight((h) => Math.max(h - 1, 0));
      } else if (e.key === "Enter") {
        // A focused button owns its own Enter (Close, Regenerate, a take
        // tile) — committing the highlight as well would double-act.
        if (target?.closest("button")) return;
        e.preventDefault();
        const t = list[highlight];
        if (t && t.id !== props.selectedTakeId) select.mutate(t.id);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [list, highlight, select, props.selectedTakeId, playingVersion]);

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
                <div className="promote-row">
                  <button
                    className="btn secondary chip-add"
                    title="Play this take"
                    onClick={() => setPlayingVersion(t.versionId)}
                  >
                    ▶
                  </button>
                  <button
                    className="btn secondary chip-add"
                    title="Open the generate panel pre-filled from this take's recipe"
                    onClick={() => props.onRegenerate(t.recipeJson)}
                  >
                    ♻ Regenerate from this
                  </button>
                </div>
              </div>
            );
          })}
        </div>
        {select.isError && <div className="status error">{String(select.error)}</div>}
        {playingVersion && (
          <ClipPlayer versionId={playingVersion} title="Take" onClose={() => setPlayingVersion(undefined)} />
        )}
      </div>
    </div>
  );
}

function recipeSummary(recipeJson: string): string {
  try {
    const r = JSON.parse(recipeJson) as { model?: string; request?: { prompt?: string } };
    // Seed extracted from the raw JSON — parsing coerces int64 to a double.
    const seed = recipeJson.match(/"seed"\s*:\s*(\d+)/)?.[1];
    return [r.model, seed ? `seed ${seed}` : "", r.request?.prompt ?? ""].filter(Boolean).join(" · ");
  } catch {
    return "";
  }
}
