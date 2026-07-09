import type { CanvasOp } from "./ops";

export interface OpSyncTransport {
  /** AppendOps: returns the new head seq. Must throw with kind "conflict"
   * (see isConflict) when base_seq is stale. `keepalive` asks for a
   * survive-page-unload send (browser keepalive fetch). */
  append(baseSeq: number, payloads: string[], opts?: { keepalive?: boolean }): Promise<number>;
  /** GetCanvas ops page: everything after `seq` (payload JSON strings, ascending). */
  fetchSince(seq: number): Promise<{ headSeq: number; payloads: string[] }>;
}

export type SyncStatus = "saved" | "pending" | "saving" | "error";

const FLUSH_DEBOUNCE_MS = 800;
const MAX_OPS_PER_APPEND = 200; // server cap
const MAX_CONSECUTIVE_CONFLICTS = 5; // livelock backstop, not a real limit

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
  /** After a failure: whether a retry is scheduled (false = ops are kept
   * locally but the server called the batch invalid — resending can't help). */
  retrying = false;
  onStatus?: (s: SyncStatus, error?: string) => void;
  onRemoteOps?: (payloads: string[]) => void;

  private pending: CanvasOp[] = [];
  private timer: ReturnType<typeof setTimeout> | undefined;
  private inFlightP: Promise<void> | null = null;
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

  /**
   * Flush everything now (page hide, export, close). The returned promise
   * resolves only when every op pending at call time has been sent (or the
   * flush failed) — callers doing "flush then act" can trust the await even
   * when a flush is already in flight.
   */
  flush(urgent = false): Promise<void> {
    if (this.inFlightP) {
      // Chain behind the in-flight run, then drain whatever it left behind.
      return this.inFlightP.then(() => this.flush(urgent));
    }
    if (this.pending.length === 0) {
      this.setStatus("saved");
      return Promise.resolve();
    }
    this.inFlightP = this.doFlush(urgent).finally(() => {
      this.inFlightP = null;
    });
    return this.inFlightP;
  }

  private async doFlush(urgent: boolean): Promise<void> {
    clearTimeout(this.timer);
    let conflicts = 0;
    while (this.pending.length > 0) {
      this.setStatus("saving");
      const batch = this.pending.slice(0, MAX_OPS_PER_APPEND);
      try {
        const head = await this.transport.append(
          this.baseSeq,
          batch.map((op) => JSON.stringify(op)),
          { keepalive: urgent },
        );
        this.pending = this.pending.slice(batch.length);
        this.baseSeq = head;
        conflicts = 0;
      } catch (err) {
        if (isConflict(err) && ++conflicts <= MAX_CONSECUTIVE_CONFLICTS) {
          // Another writer appended after baseSeq: pull what we missed, let
          // the doc replay it, and retry on the new head. The "other writer"
          // can be OUR OWN lost-ack append — an op that committed but whose
          // response never arrived — so prune pending ops the refetch already
          // contains, or a network blip would write them to the durable log
          // twice.
          try {
            const { headSeq, payloads } = await this.transport.fetchSince(this.baseSeq);
            this.onRemoteOps?.(payloads);
            const landed = new Set(payloads.map(payloadOpId).filter(Boolean));
            this.pending = this.pending.filter((op) => !landed.has(op.op_id));
            this.baseSeq = headSeq;
          } catch (refetchErr) {
            this.fail(refetchErr);
            return;
          }
        } else {
          this.fail(err);
          return;
        }
      }
    }
    this.setStatus("saved");
  }

  get pendingCount(): number {
    return this.pending.length;
  }

  private fail(err: unknown) {
    // The server rejected the batch as invalid: retrying the same bytes can
    // never succeed — keep the ops (undo still works) but stop hammering.
    // Everything else is presumed transient; ops stay queued and retry.
    this.retrying = !isInvalid(err);
    this.setStatus("error", String(err));
    clearTimeout(this.timer);
    if (this.retrying) {
      this.timer = setTimeout(() => void this.flush(), 5000);
    }
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

function isInvalid(err: unknown): boolean {
  const e = err as { code?: unknown; message?: unknown };
  return (
    e?.code === "invalid_argument" ||
    e?.code === 3 || // connect-es Code.InvalidArgument
    (typeof e?.message === "string" && e.message.includes("[invalid_argument]"))
  );
}

function payloadOpId(payload: string): string | undefined {
  try {
    const op = JSON.parse(payload) as { op_id?: string };
    return typeof op?.op_id === "string" ? op.op_id : undefined;
  } catch {
    return undefined;
  }
}
