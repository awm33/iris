import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
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

  const latest = exports.data?.exports[0];
  const busy = latest?.state === "queued" || latest?.state === "running";

  // Download is a REAL anchor with a pre-signed href: signing inside a
  // click handler puts window.open after an await, where Safari always —
  // and Chrome eventually — blocks it as a popup. Presigns expire (15m),
  // so refresh well inside that.
  const dl = useQuery({
    queryKey: ["exportDl", latest?.versionId ?? ""],
    enabled: latest?.state === "complete" && !!latest.versionId,
    staleTime: 5 * 60_000,
    refetchInterval: 10 * 60_000,
    queryFn: async () => (await assetClient.signDownload({ versionId: latest!.versionId })).url,
  });

  // The export lands as a library asset — surface it there without a remount.
  const prevState = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (prevState.current && prevState.current !== "complete" && latest?.state === "complete") {
      void qc.invalidateQueries({ queryKey: ["assets"] });
    }
    prevState.current = latest?.state;
  }, [latest?.state, qc]);

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
      {latest?.state === "complete" && dl.data && (
        <a
          className="btn secondary"
          href={dl.data}
          target="_blank"
          rel="noreferrer"
          title={`Download the last export (${latest.preset})`}
        >
          ⬇ {latest.preset}.mp4
        </a>
      )}
    </span>
  );
}
