import { useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";

// Subscribes to the backend's SSE bridge (/events → pg NOTIFY relay) and
// invalidates the affected queries — jobs on generation events; assets and
// thumbnails on media events (posters appearing after the probe). Payloads
// are thin {channel, id}; the API stays the single source of truth.
export function useEvents() {
  const qc = useQueryClient();
  useEffect(() => {
    const es = new EventSource("/events");
    es.onmessage = (m) => {
      try {
        const ev = JSON.parse(m.data) as { channel: string };
        if (ev.channel === "generation_jobs") {
          void qc.invalidateQueries({ queryKey: ["jobs"] });
          void qc.invalidateQueries({ queryKey: ["assets"] });
        } else if (ev.channel === "media_jobs") {
          void qc.invalidateQueries({ queryKey: ["assets"] });
          void qc.invalidateQueries({ queryKey: ["thumb"] });
          void qc.invalidateQueries({ queryKey: ["artifact-thumb"] });
        }
      } catch {
        // ignore malformed frames; the next event resyncs
      }
    };
    return () => es.close();
  }, [qc]);
}
