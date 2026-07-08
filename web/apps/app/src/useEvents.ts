import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import { useEffect } from "react";

// Subscribes to the backend's SSE bridge (/events → pg NOTIFY relay) and
// invalidates the affected queries. SSE is an accelerator, never load-bearing
// for correctness:
//  - onopen invalidates everything (EventSource auto-reconnects after API
//    restarts; NOTIFYs fired in the gap are unrecoverable — resync instead)
//  - invalidations are debounced (trailing 300ms) so a fan-out burst
//    (~25 NOTIFYs for an 8-take job) coalesces into one refetch round
//  - thumbnail queries are only refetched when errored (poster-pending);
//    successful signed URLs don't change on new events
//  - App.tsx adds a slow poll while jobs are active as the final backstop
export function useEvents() {
  const qc = useQueryClient();
  useEffect(() => {
    let genDirty = false;
    let mediaDirty = false;
    let timer: number | undefined;

    const flush = () => {
      timer = undefined;
      if (genDirty) {
        genDirty = false;
        void qc.invalidateQueries({ queryKey: ["jobs"] });
        void qc.invalidateQueries({ queryKey: ["assets"] });
      }
      if (mediaDirty) {
        mediaDirty = false;
        void qc.invalidateQueries({ queryKey: ["assets"] });
        invalidateErroredThumbs(qc);
      }
    };
    const schedule = () => {
      if (timer === undefined) timer = window.setTimeout(flush, 300);
    };

    const es = new EventSource("/events");
    es.onopen = () => {
      // Fresh or re-established stream: anything may have changed meanwhile.
      genDirty = true;
      mediaDirty = true;
      schedule();
    };
    es.onmessage = (m) => {
      try {
        const ev = JSON.parse(m.data) as { channel: string };
        if (ev.channel === "generation_jobs") genDirty = true;
        else if (ev.channel === "media_jobs") mediaDirty = true;
        else return;
        schedule();
      } catch {
        // ignore malformed frames; the next event resyncs
      }
    };
    return () => {
      if (timer !== undefined) window.clearTimeout(timer);
      es.close();
    };
  }, [qc]);
}

function invalidateErroredThumbs(qc: QueryClient) {
  const predicate = (q: { state: { status: string } }) => q.state.status === "error";
  void qc.invalidateQueries({ queryKey: ["thumb"], predicate });
  void qc.invalidateQueries({ queryKey: ["artifact-thumb"], predicate });
}
