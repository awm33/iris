import { useQuery } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState } from "react";
import { canvasClient, storyClient, timelineClient } from "../api";
import { useEscape } from "./AssetThumb";

// ⌘K command palette (UX doc §3.1 header): navigation, actions, and
// entity search in one affordance. Commands are provided by the shell;
// entities load lazily from the same caches the pages use.

export interface PaletteCommand {
  label: string;
  hint?: string;
  run: () => void;
}

export function CommandPalette(props: {
  projectId?: string;
  commands: PaletteCommand[];
  onOpenScene: (id: string) => void;
  onOpenCanvas: (id: string) => void;
  onOpenTimeline: (id: string) => void;
  onClose: () => void;
}) {
  useEscape(props.onClose);
  const [q, setQ] = useState("");
  const [highlight, setHighlight] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  useEffect(() => inputRef.current?.focus(), []);

  const pid = props.projectId ?? "";
  const scenes = useQuery({
    queryKey: ["scenes", pid],
    enabled: !!pid,
    queryFn: () => storyClient.listScenes({ projectId: pid }),
  });
  const canvases = useQuery({
    queryKey: ["canvases", pid],
    enabled: !!pid,
    queryFn: () => canvasClient.listCanvases({ projectId: pid }),
  });
  const timelines = useQuery({
    queryKey: ["timelines", pid],
    enabled: !!pid,
    queryFn: () => timelineClient.listTimelines({ projectId: pid }),
  });

  const items = useMemo<PaletteCommand[]>(() => {
    const entities: PaletteCommand[] = [
      ...(scenes.data?.scenes ?? []).map((s) => ({
        label: s.name,
        hint: "scene",
        run: () => props.onOpenScene(s.id),
      })),
      ...(canvases.data?.canvases ?? []).map((c) => ({
        label: c.name,
        hint: "canvas",
        run: () => props.onOpenCanvas(c.id),
      })),
      ...(timelines.data?.timelines ?? []).map((t) => ({
        label: t.name,
        hint: "timeline",
        run: () => props.onOpenTimeline(t.id),
      })),
    ];
    const all = [...props.commands, ...entities];
    const needle = q.trim().toLowerCase();
    if (!needle) return all.slice(0, 12);
    // Subsequence match keeps it forgiving without a fuzzy dependency.
    const matches = (s: string) => {
      let i = 0;
      for (const ch of s.toLowerCase()) if (ch === needle[i]) i++;
      return i >= needle.length || s.toLowerCase().includes(needle);
    };
    return all.filter((c) => matches(c.label) || (c.hint && c.hint.includes(needle))).slice(0, 12);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [q, props.commands, scenes.data, canvases.data, timelines.data]);

  useEffect(() => setHighlight(0), [q]);

  const run = (c: PaletteCommand) => {
    props.onClose();
    c.run();
  };

  return (
    <div className="overlay palette-overlay" onClick={props.onClose}>
      <div className="palette" role="dialog" aria-modal="true" aria-label="Command palette" onClick={(e) => e.stopPropagation()}>
        <input
          ref={inputRef}
          type="text"
          placeholder="Jump to… or run a command"
          value={q}
          onChange={(e) => setQ(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "ArrowDown") {
              e.preventDefault();
              setHighlight((h) => Math.min(h + 1, items.length - 1));
            } else if (e.key === "ArrowUp") {
              e.preventDefault();
              setHighlight((h) => Math.max(h - 1, 0));
            } else if (e.key === "Enter" && items[highlight]) {
              run(items[highlight]);
            }
          }}
        />
        <div className="palette-list">
          {items.map((c, i) => (
            <button
              key={`${c.hint ?? "cmd"}:${c.label}:${i}`}
              className={`palette-item${i === highlight ? " highlight" : ""}`}
              onMouseEnter={() => setHighlight(i)}
              onClick={() => run(c)}
            >
              <span className="truncate">{c.label}</span>
              {c.hint && <span className="meta">{c.hint}</span>}
            </button>
          ))}
          {items.length === 0 && <div className="empty">No matches.</div>}
        </div>
      </div>
    </div>
  );
}
