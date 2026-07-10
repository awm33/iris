import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { assetClient, timelineClient } from "../api";

// Export v1 (M7): kick a server render and surface its lifecycle inline.
// The render is a media job — no SSE for those, so the list slow-polls
// only while something is in flight (same backstop pattern as JobsPage).

export function ExportControl(props: { timelineId: string }) {
  const qc = useQueryClient();
  const [preset, setPreset] = useState("draft");
  const exports = useQuery({
    queryKey: ["exports", props.timelineId],
    queryFn: () => timelineClient.listExports({ timelineId: props.timelineId }),
    refetchInterval: (q) =>
      q.state.data?.exports.some((e) => e.state === "queued" || e.state === "running") ? 3000 : false,
  });
  const start = useMutation({
    mutationFn: () => timelineClient.startExport({ timelineId: props.timelineId, preset }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["exports", props.timelineId] }),
  });
  const download = useMutation({
    mutationFn: async (versionId: string) => (await assetClient.signDownload({ versionId })).url,
    onSuccess: (url) => window.open(url, "_blank"),
  });

  const latest = exports.data?.exports[0];
  const busy = latest?.state === "queued" || latest?.state === "running";

  return (
    <span className="export-control">
      <select value={preset} onChange={(e) => setPreset(e.target.value)} title="Export preset">
        <option value="draft">draft 720p</option>
        <option value="master">master 1080p</option>
      </select>
      <button
        className="btn secondary"
        disabled={start.isPending || busy}
        title="Render this timeline to an H.264 mp4 (lands in the Library)"
        onClick={() => start.mutate()}
      >
        {busy ? "⏳ exporting…" : "⬇ Export"}
      </button>
      {start.isError && <span className="status error">{String(start.error)}</span>}
      {latest?.state === "failed" && (
        <span className="status error truncate" style={{ maxWidth: 280 }} title={latest.error}>
          export failed: {latest.error}
        </span>
      )}
      {latest?.state === "complete" && latest.versionId && (
        <button
          className="btn secondary"
          disabled={download.isPending}
          title={`Download the last export (${latest.preset})`}
          onClick={() => download.mutate(latest.versionId)}
        >
          ⬇ {latest.preset}.mp4
        </button>
      )}
    </span>
  );
}
