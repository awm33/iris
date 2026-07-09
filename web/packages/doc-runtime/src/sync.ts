import type { CanvasOp } from "./ops";

export interface OpSyncTransport {
  /** AppendOps: returns the new head seq. Must throw with kind "conflict"
   * (see isConflict) when base_seq is stale. */
  append(baseSeq: number, payloads: string[]): Promise<number>;
  /** GetCanvas ops page: everything after `seq` (payload JSON strings, ascending). */
  fetchSince(seq: number): Promise<{ headSeq: number; payloads: string[] }>;
}

export type SyncStatus = "saved" | "pending" | "saving" | "error";

const FLUSH_DEBOUNCE_MS = 800;
const MAX_OPS_PER_APPEND = 200; // server cap

/**
 * Autosave batcher: locally-authored ops queue here and flush debounced, in
 * order, with optimistic concurrency on base_seq. On conflict (another tab
 * appended first) it fetches the missed ops, hands them to the doc, rebases,
 * and retries — op logs append; there is nothing to merge.
 */
export class OpSync {
  status: SyncStatus = "saved";
  /** Last server seq our log is built on (grows with every ack/refetch). */
  baseSeq: number;
  onStatus?: (s: SyncStatus, error?: string) => void;
  onRemoteOps?: (payloads: string[]) => void;

  private pending: CanvasOp[] = [];
  private timer: ReturnType<typeof setTimeout> | undefined;
  private inFlight = false;
  private transport: OpSyncTransport;

  constructor(transport: OpSyncTransport, baseSeq: number) {
    this.transport = transport;
    this.baseSeq = baseSeq;
  }

  enqueue(op: CanvasOp) {
    this.pending.push(op);
    this.setStatus("pending");
    clearTimeout(this.timer);
    this.timer = setTimeout(() => void this.flush(), FLUSH_DEBOUNCE_MS);
  }

  /** Flush everything now (page hide, export, close). */
  async flush(): Promise<void> {
    if (this.inFlight) return; // the in-flight completion re-flushes leftovers
    if (this.pending.length === 0) {
      this.setStatus("saved");
      return;
    }
    clearTimeout(this.timer);
    this.inFlight = true;
    this.setStatus("saving");
    const batch = this.pending.slice(0, MAX_OPS_PER_APPEND);
    try {
      const head = await this.transport.append(
        this.baseSeq,
        batch.map((op) => JSON.stringify(op)),
      );
      this.pending = this.pending.slice(batch.length);
      this.baseSeq = head;
    } catch (err) {
      if (isConflict(err)) {
        // Another writer appended after baseSeq: pull what we missed, let the
        // doc replay it, and retry our batch on the new head.
        try {
          const { headSeq, payloads } = await this.transport.fetchSince(this.baseSeq);
          this.onRemoteOps?.(payloads);
          this.baseSeq = headSeq;
        } catch (refetchErr) {
          this.fail(refetchErr);
          return;
        }
      } else {
        this.fail(err);
        return;
      }
    } finally {
      this.inFlight = false;
    }
    if (this.pending.length > 0) {
      await this.flush(); // leftovers or the rebased batch
    } else {
      this.setStatus("saved");
    }
  }

  get pendingCount(): number {
    return this.pending.length;
  }

  private fail(err: unknown) {
    this.inFlight = false;
    this.setStatus("error", String(err));
    // Ops stay queued; a later enqueue or manual flush retries.
    clearTimeout(this.timer);
    this.timer = setTimeout(() => void this.flush(), 5000);
  }

  private setStatus(s: SyncStatus, error?: string) {
    this.status = s;
    this.onStatus?.(s, error);
  }
}

/** Transport marks base_seq conflicts by throwing an error whose `code` or
 * message says "aborted" (Connect's CodeAborted maps here). */
export function isConflict(err: unknown): boolean {
  const e = err as { code?: unknown; message?: unknown };
  return (
    e?.code === "aborted" ||
    e?.code === 10 || // connect-es Code.Aborted
    (typeof e?.message === "string" && e.message.includes("[aborted]"))
  );
}
