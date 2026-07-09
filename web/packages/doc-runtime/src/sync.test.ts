import { describe, expect, it } from "vitest";
import type { CanvasOp } from "./ops";
import { OpSync, type OpSyncTransport } from "./sync";

const op = (id: string): CanvasOp => ({
  op_id: id,
  type: "stroke",
  layer_id: "a",
  tool: "brush",
  color: "#fff",
  size: 1,
  points: [[0, 0]],
});

class ConnectAborted extends Error {
  code = 10;
}

describe("OpSync", () => {
  it("flushes queued ops in order and advances baseSeq", async () => {
    const appended: { base: number; count: number }[] = [];
    let head = 5;
    const transport: OpSyncTransport = {
      append: async (base, payloads) => {
        appended.push({ base, count: payloads.length });
        head = base + payloads.length;
        return head;
      },
      fetchSince: async () => ({ headSeq: head, payloads: [] }),
    };
    const sync = new OpSync(transport, 5);
    sync.enqueue(op("a"));
    sync.enqueue(op("b"));
    await sync.flush();
    expect(appended).toEqual([{ base: 5, count: 2 }]);
    expect(sync.baseSeq).toBe(7);
    expect(sync.status).toBe("saved");
    expect(sync.pendingCount).toBe(0);
  });

  it("on conflict: fetches missed ops, hands them over, rebases, retries", async () => {
    let conflicted = false;
    const bases: number[] = [];
    const remote: string[] = [];
    const transport: OpSyncTransport = {
      append: async (base, payloads) => {
        bases.push(base);
        if (!conflicted) {
          conflicted = true;
          throw new ConnectAborted("[aborted] base_seq is stale");
        }
        return base + payloads.length;
      },
      fetchSince: async (seq) => {
        expect(seq).toBe(0);
        return { headSeq: 3, payloads: ["r1", "r2", "r3"] };
      },
    };
    const sync = new OpSync(transport, 0);
    sync.onRemoteOps = (ps) => remote.push(...ps);
    sync.enqueue(op("mine"));
    await sync.flush();
    expect(bases).toEqual([0, 3]); // first try at stale base, retry on new head
    expect(remote).toEqual(["r1", "r2", "r3"]);
    expect(sync.baseSeq).toBe(4);
    expect(sync.status).toBe("saved");
  });

  it("lost ack: prunes pending ops the refetch already contains (no duplicate append)", async () => {
    // The op COMMITTED server-side but the response was lost; the retry
    // conflicts, the refetch returns our own op — it must not be re-sent.
    let call = 0;
    const appended: string[][] = [];
    const transport: OpSyncTransport = {
      append: async (base, payloads) => {
        call++;
        if (call === 1) throw new ConnectAborted("[aborted] base_seq is stale"); // ack was lost
        appended.push(payloads);
        return base + payloads.length;
      },
      fetchSince: async () => ({
        headSeq: 1,
        payloads: [JSON.stringify(op("mine"))], // our own op, already landed
      }),
    };
    const sync = new OpSync(transport, 0);
    sync.enqueue(op("mine"));
    await sync.flush();
    expect(appended).toEqual([]); // nothing re-sent
    expect(sync.pendingCount).toBe(0);
    expect(sync.baseSeq).toBe(1);
    expect(sync.status).toBe("saved");
  });

  it("keeps ops queued on hard errors and reports status", async () => {
    const transport: OpSyncTransport = {
      append: async () => {
        throw new Error("boom");
      },
      fetchSince: async () => ({ headSeq: 0, payloads: [] }),
    };
    const sync = new OpSync(transport, 0);
    const statuses: string[] = [];
    sync.onStatus = (s) => statuses.push(s);
    sync.enqueue(op("a"));
    await sync.flush();
    expect(sync.status).toBe("error");
    expect(sync.pendingCount).toBe(1); // nothing lost
    expect(statuses).toContain("saving");
  });

  it("splits oversized queues into server-cap batches", async () => {
    const counts: number[] = [];
    const transport: OpSyncTransport = {
      append: async (base, payloads) => {
        counts.push(payloads.length);
        return base + payloads.length;
      },
      fetchSince: async () => ({ headSeq: 0, payloads: [] }),
    };
    const sync = new OpSync(transport, 0);
    for (let i = 0; i < 250; i++) sync.enqueue(op(`o${i}`));
    await sync.flush();
    expect(counts).toEqual([200, 50]);
    expect(sync.baseSeq).toBe(250);
  });
});
