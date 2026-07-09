import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { timelineClient } from "../api";

import { newOpId as oid } from "@iris/doc-runtime";

export function TimelinesPage(props: { projectId: string; onOpen: (id: string) => void }) {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const timelines = useQuery({
    queryKey: ["timelines", props.projectId],
    queryFn: () => timelineClient.listTimelines({ projectId: props.projectId }),
  });
  const create = useMutation({
    mutationFn: async (n: string) => {
      const r = await timelineClient.createTimeline({ projectId: props.projectId, name: n });
      try {
        // Seed V1 + A1 so the first clip just works (canvas paint-layer pattern).
        await timelineClient.appendTimelineOps({
        timelineId: r.timeline!.id,
        baseSeq: 0n,
        payloads: [
          JSON.stringify({ op_id: oid(), type: "add_track", track: { id: `trk_${oid().slice(3)}`, kind: "video", name: "V1" } }),
          JSON.stringify({ op_id: oid(), type: "add_track", track: { id: `trk_${oid().slice(3)}`, kind: "audio", name: "A1" } }),
        ],
        });
      } catch (err) {
        // Don't strand an orphan with no tracks (there is no add-track UI).
        await timelineClient.deleteTimeline({ id: r.timeline!.id }).catch(() => {});
        throw err;
      }
      return r;
    },
    onSuccess: (r) => {
      setName("");
      void qc.invalidateQueries({ queryKey: ["timelines"] });
      if (r.timeline) props.onOpen(r.timeline.id);
    },
  });
  return (
    <div>
      <h2>Timelines</h2>
      <div className="toolbar">
        <input type="text" placeholder="New timeline name…" value={name}
          onChange={(e) => setName(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && name.trim() && create.mutate(name.trim())} />
        <button className="btn" disabled={!name.trim() || create.isPending} onClick={() => create.mutate(name.trim())}>
          {create.isPending ? "Creating…" : "New timeline"}
        </button>
        {create.isError && <span className="status error">{String(create.error)}</span>}
      </div>
      {timelines.data?.timelines.length === 0 && <div className="empty">No timelines yet.</div>}
      <div className="grid">
        {timelines.data?.timelines.map((t) => (
          <div key={t.id} className="card" onClick={() => props.onOpen(t.id)}>
            <div className="thumb-placeholder">🎞</div>
            <div className="name">{t.name}</div>
            <div className="meta">{t.fps} fps · {t.headSeq.toString()} ops</div>
          </div>
        ))}
      </div>
    </div>
  );
}
