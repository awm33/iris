import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef } from "react";
import { timelineClient } from "../api";

// Transcribe (M7): server renders the timeline's mixed audio, runs the
// configured speech-to-text engine, and appends the result to the doc as
// caption ops. Lifecycle polling mirrors ExportControl; onComplete fires
// once per completion so the page can reload the doc (the appended ops
// exist only server-side).

export function TranscribeControl(props: { timelineId: string; onComplete: () => void }) {
  const qc = useQueryClient();
  const list = useQuery({
    queryKey: ["transcriptions", props.timelineId],
    queryFn: () => timelineClient.listTranscriptions({ timelineId: props.timelineId }),
    refetchInterval: (q) =>
      q.state.data?.transcriptions.some((t) => t.state === "queued" || t.state === "running") ? 3000 : false,
  });
  const start = useMutation({
    mutationFn: () => timelineClient.startTranscription({ timelineId: props.timelineId }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["transcriptions", props.timelineId] }),
  });

  const latest = list.data?.transcriptions[0];
  const busy = latest?.state === "queued" || latest?.state === "running";

  const prevState = useRef<string | undefined>(undefined);
  useEffect(() => {
    // A surviving instance switching timelines must not carry "running"
    // over — timeline B's already-complete latest would fire a spurious
    // reload.
    prevState.current = undefined;
  }, [props.timelineId]);
  useEffect(() => {
    if (prevState.current && prevState.current !== "complete" && latest?.state === "complete") {
      props.onComplete();
    }
    prevState.current = latest?.state;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [latest?.state]);

  return (
    <span className="export-control">
      <button
        className="btn secondary"
        disabled={start.isPending || busy}
        title="Transcribe the timeline's audio into an editable caption track"
        onClick={() => start.mutate()}
      >
        {busy ? "⏳ transcribing…" : "📝 Transcribe"}
      </button>
      {start.isError && <span className="status error">{String(start.error)}</span>}
      {latest?.state === "failed" && (
        <span className="status error truncate" style={{ maxWidth: 240 }} title={latest.error}>
          transcribe failed: {latest.error}
        </span>
      )}
    </span>
  );
}
