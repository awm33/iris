import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { type Canvas, JobState } from "@iris/api-client";
import {
  CanvasDoc,
  type CanvasOp,
  type LayerState,
  newOpId,
  OpSync,
  type OpSyncTransport,
  parseOp,
  type SyncStatus,
} from "@iris/doc-runtime";
import { assetClient, canvasClient, canvasKeepaliveClient, generationClient, storyClient, uploadFile } from "../api";
import { isActiveJob } from "../jobBadges";
import { flatten, LayerRasterCache } from "./renderer";
import { CanvasViewport, type CanvasTool } from "./CanvasViewport";
import { GenFillBar, type GenFillState } from "./GenFillBar";
import {
  bitmapSelectionFromMask,
  type GenFillEndpoint,
  genFillEndpoints,
  pickProfile,
  removalDilation,
  renderMaskBlob,
  type Selection,
} from "./genfill";

interface Session {
  canvas: Canvas;
  doc: CanvasDoc;
  sync: OpSync;
  cache: LayerRasterCache;
}

const parseOps = (payloads: string[]): CanvasOp[] =>
  payloads.map(parseOp).filter((o): o is CanvasOp => o !== null);

export function CanvasPage(props: { canvasId: string; projectId: string; onBack: () => void }) {
  const qc = useQueryClient();
  const [session, setSession] = useState<Session | null>(null);
  const [loadError, setLoadError] = useState<string>();
  const [status, setStatus] = useState<SyncStatus>("saved");
  const [syncError, setSyncError] = useState<string>();
  const [tool, setTool] = useState<CanvasTool>("brush");
  const [color, setColor] = useState("#ff5a36");
  const [brushSize, setBrushSize] = useState(24);
  const [activeLayerId, setActiveLayerId] = useState<string>();
  const [, setDocTick] = useState(0); // re-render the layers panel on doc change
  const [redrawTick, setRedrawTick] = useState(0);
  const [preview, setPreview] = useState<{ layerId: string; opacity: number }>();
  const [exportMsg, setExportMsg] = useState<string>();
  const [exporting, setExporting] = useState(false);
  const [imageError, setImageError] = useState<string>();
  const [retrying, setRetrying] = useState(true);
  const [selection, setSelection] = useState<Selection>();
  const [genFill, setGenFill] = useState<GenFillState>();
  const [genFillError, setGenFillError] = useState<string>();
  const [promoting, setPromoting] = useState(false);
  const [subjectBusy, setSubjectBusy] = useState(false);
  // Subject-select session: the flatten is uploaded once and reused until
  // the doc changes (ops length moves); clicks accumulate as refine points.
  const subjectRef = useRef<{ versionId: string; opsLen: number; points: { x: number; y: number; negative: boolean }[] } | null>(null);
  // Bumped by discard: a gen-fill submit in flight checks it after every
  // await and abandons itself (see submitGenFill).
  const submitNonce = useRef(0);

  // Image-layer pixels: versionId → Image, loaded via signed URLs.
  // crossOrigin=anonymous keeps the canvas untainted so export can read it.
  const images = useRef(new Map<string, { img: HTMLImageElement; loaded: boolean }>());
  const sessionRef = useRef<Session | null>(null);
  sessionRef.current = session;
  const getImage = (versionId: string): HTMLImageElement | null => {
    let e = images.current.get(versionId);
    if (!e) {
      const img = new Image();
      img.crossOrigin = "anonymous";
      e = { img, loaded: false };
      images.current.set(versionId, e);
      const failed = () => {
        // Drop the entry so the next composite retries the load.
        images.current.delete(versionId);
        setImageError("A layer image failed to load — retrying on next redraw.");
      };
      img.onerror = failed;
      void assetClient
        .signDownload({ versionId })
        .then((r) => {
          img.onload = () => {
            e!.loaded = true;
            setImageError(undefined);
            const s = sessionRef.current;
            if (s) s.cache.invalidateImage(versionId, s.doc.state);
            setRedrawTick((t) => t + 1);
          };
          img.src = r.url;
        })
        .catch(failed);
    }
    return e.loaded ? e.img : null;
  };

  // The canvases list shows op counts — refresh it however the editor is
  // left (toolbar ←, browser Back, palette jump), not just the one onBack
  // callback App happens to wire.
  useEffect(
    () => () => {
      void qc.invalidateQueries({ queryKey: ["canvases"] });
    },
    [qc],
  );

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        // Page through the op log until we hold everything up to head_seq.
        let after = 0n;
        let canvas: Canvas | undefined;
        const payloads: string[] = [];
        for (;;) {
          const r = await canvasClient.getCanvas({ id: props.canvasId, afterSeq: after });
          canvas = r.canvas!;
          for (const op of r.ops) payloads.push(op.payload);
          if (r.ops.length === 0 || r.ops[r.ops.length - 1].seq >= canvas.headSeq) break;
          after = r.ops[r.ops.length - 1].seq;
        }
        if (cancelled || !canvas) return;

        const doc = new CanvasDoc(parseOps(payloads));
        const cache = new LayerRasterCache(canvas.width, canvas.height, getImage);
        const transport: OpSyncTransport = {
          append: async (baseSeq, ps, opts) => {
            // keepalive only for unload-time flushes: it survives tab close
            // but the platform caps its body at 64KB (a big batch just fails
            // and stays pending — no worse than a killed ordinary fetch).
            const client = opts?.keepalive ? canvasKeepaliveClient : canvasClient;
            const r = await client.appendOps({
              canvasId: props.canvasId,
              baseSeq: BigInt(baseSeq),
              payloads: ps,
            });
            return Number(r.headSeq);
          },
          fetchSince: async (seq) => {
            let cursor = BigInt(seq);
            const missed: string[] = [];
            let head = seq;
            for (;;) {
              const r = await canvasClient.getCanvas({ id: props.canvasId, afterSeq: cursor });
              for (const op of r.ops) missed.push(op.payload);
              head = Number(r.canvas!.headSeq);
              if (r.ops.length === 0 || r.ops[r.ops.length - 1].seq >= r.canvas!.headSeq) break;
              cursor = r.ops[r.ops.length - 1].seq;
            }
            return { headSeq: head, payloads: missed };
          },
        };
        const sync = new OpSync(transport, Number(canvas.headSeq));
        doc.onLocalOp = (op) => sync.enqueue(op);
        sync.onRemoteOps = (ps) => doc.applyRemote(parseOps(ps));
        sync.onStatus = (s, err) => {
          setStatus(s);
          setSyncError(err);
          setRetrying(sync.retrying);
        };
        setSession({ canvas, doc, sync, cache });
        const layers = doc.state.layers;
        const topPaint = [...layers].reverse().find((l) => l.kind === "paint");
        setActiveLayerId((topPaint ?? layers[layers.length - 1])?.id);
      } catch (e) {
        if (!cancelled) setLoadError(String(e));
      }
    })();
    return () => {
      cancelled = true;
    };
    // getImage is stable-enough (ref-backed); the session is rebuilt per canvas id.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [props.canvasId]);

  // Layers panel re-renders on every doc change; pending ops flush on leave.
  useEffect(() => {
    if (!session) return;
    const unsub = session.doc.subscribe(() => setDocTick((t) => t + 1));
    // urgent=true → keepalive fetch: an ordinary fetch is killed on unload
    // and the debounced batch would be silently lost.
    const flush = () => void session.sync.flush(true);
    const onBeforeUnload = (e: BeforeUnloadEvent) => {
      flush();
      // Ops the server REJECTED (not a transient outage) can only be saved
      // by user action — closing the tab silently discards them. Ask.
      if (session.sync.status === "error" && !session.sync.retrying && session.sync.pendingCount > 0) {
        e.preventDefault();
      }
    };
    window.addEventListener("beforeunload", onBeforeUnload);
    document.addEventListener("visibilitychange", flush);
    return () => {
      unsub();
      window.removeEventListener("beforeunload", onBeforeUnload);
      document.removeEventListener("visibilitychange", flush);
      flush();
    };
  }, [session]);

  // Cmd/Ctrl+Z undo, +Shift redo — never while typing in a field. While the
  // candidate strip is open: ←/→ compare, Enter commits, Esc discards; Esc
  // otherwise clears the selection.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement;
      if (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.isContentEditable) return;
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "z") {
        e.preventDefault();
        if (e.shiftKey) session?.doc.redo();
        else session?.doc.undo();
        return;
      }
      if (genFill?.phase === "choosing") {
        if (e.key === "ArrowRight" || e.key === "ArrowLeft") {
          e.preventDefault();
          const d = e.key === "ArrowRight" ? 1 : -1;
          setGenFill({
            ...genFill,
            index: (genFill.index + d + genFill.candidates.length) % genFill.candidates.length,
          });
        } else if (e.key === "Enter") {
          e.preventDefault();
          commitCandidate();
        } else if (e.key === "Escape") {
          setGenFill(undefined);
        }
        return;
      }
      // Esc never clears the selection while a gen-fill is in flight — the
      // vanishing marching ants would read as a cancel the flow didn't do.
      if (e.key === "Escape" && selection && !genFill) {
        setSelection(undefined);
        // Fresh subject next time: refine points don't outlive the selection.
        if (subjectRef.current) subjectRef.current.points = [];
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  });

  // Gen-fill stances, stated once (v1, single-user):
  // - Navigating away mid-generation cancels nothing; on RETURN the canvas
  //   re-attaches to a still-ACTIVE inpaint job targeting it (the poll below
  //   then carries it into choosing as usual). Jobs that finished while away
  //   stay in Library/Jobs — resurrecting a completed strip would also
  //   resurrect every strip the user already discarded.
  // - Editing while generating is allowed; the eventual commit is a masked
  //   layer over the doc AS IT IS THEN — "fill this region as it was when I
  //   asked". Strokes painted inside the selection meanwhile end up occluded
  //   (undo or hide-layer reveals them).
  // - Esc in choosing returns to the prompt bar with the selection armed
  //   (retry loop); a second Esc clears the selection.
  // Gen-fill capable endpoints (manifest-negotiated).
  const endpoints = useQuery({
    queryKey: ["endpoints"],
    staleTime: 60_000,
    queryFn: () => generationClient.listModelEndpoints({}),
  });
  const gfEndpoints = genFillEndpoints(endpoints.data?.endpoints ?? []);

  // Re-attach on mount: an ACTIVE inpaint job targeting this canvas (the
  // user backed out mid-generation and returned) resumes the generating UI.
  // The job's echoed conditioning carries the mask refs commit needs.
  const reattached = useRef(false);
  useEffect(() => {
    if (reattached.current || !session) return;
    reattached.current = true;
    void generationClient.listJobs({ projectId: props.projectId }).then((r) => {
      // Newest-first; only ever adopt the most recent claim on this canvas.
      const j = r.jobs.find((x) => x.targetEntityId === props.canvasId && x.task === "inpaint");
      if (!j || !isActiveJob(j) || !j.conditioning?.mask) return;
      setGenFill((cur) =>
        cur ?? {
          phase: "generating",
          jobId: j.id,
          maskVersionId: j.conditioning!.mask!.versionId,
          maskAssetId: j.conditioning!.mask!.assetId,
          removal: j.prompt === "",
        },
      );
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session]);

  // Poll the gen-fill job while it runs (SSE invalidation accelerates this;
  // polling is the backstop, same stance as everywhere else).
  const gfJobId = genFill?.phase === "generating" ? genFill.jobId : "";
  const gfJob = useQuery({
    queryKey: ["job", gfJobId],
    enabled: gfJobId !== "",
    refetchInterval: 1500,
    queryFn: () => generationClient.getJob({ id: gfJobId }),
  });
  useEffect(() => {
    const j = gfJob.data?.job;
    if (!j || genFill?.phase !== "generating") return;
    if (j.state === JobState.COMPLETE && j.artifactVersionIds.length > 0) {
      setGenFill({
        phase: "choosing",
        jobId: genFill.jobId,
        maskVersionId: genFill.maskVersionId,
        maskAssetId: genFill.maskAssetId,
        candidates: j.artifactVersionIds,
        index: 0,
        removal: genFill.removal,
      });
    } else if (j.state === JobState.COMPLETE) {
      // Terminal with nothing to choose — without this branch the poll would
      // spin on a complete job forever with the UI stuck at "Generating…".
      setGenFillError("generation completed without candidates");
      setGenFill(undefined);
    } else if (j.state === JobState.FAILED || j.state === JobState.CANCELED) {
      setGenFillError(j.errorMessage || `generation ${j.state === JobState.FAILED ? "failed" : "was canceled"}`);
      setGenFill(undefined);
    }
  }, [gfJob.data, genFill]);

  if (loadError) return <div className="status error">Couldn’t open canvas: {loadError}</div>;
  if (!session) return <div className="empty">Opening canvas…</div>;

  const { doc, canvas, cache } = session;
  const layers = doc.state.layers;
  const active = layers.find((l) => l.id === activeLayerId);

  const commitOpacityPreview = () => {
    if (!preview) return;
    doc.apply({
      op_id: newOpId(),
      type: "set_layer",
      layer_id: preview.layerId,
      props: { opacity: preview.opacity },
    });
    setPreview(undefined);
  };

  async function subjectClick(pt: [number, number], negative: boolean) {
    if (subjectBusy || !session) return;
    setSubjectBusy(true);
    setGenFillError(undefined);
    try {
      let sess = subjectRef.current;
      if (!sess || sess.opsLen !== doc.ops.length) {
        // Doc changed since the last embed — flatten + upload fresh. Capture
        // ops length BEFORE the flatten: an op landing mid-upload must not
        // stamp the old flatten as current.
        const opsLen = doc.ops.length;
        const up = await uploadFile(await flattenToFile(" (subject)"), props.projectId);
        sess = { versionId: up.version!.id, opsLen, points: [] };
        subjectRef.current = sess;
      }
      if (sess.points.length >= 16) {
        setGenFillError("refine limit reached — Esc to restart the selection");
        return;
      }
      sess.points.push({ x: pt[0], y: pt[1], negative });
      try {
        const res = await canvasClient.subjectMask({
          versionId: sess.versionId,
          points: sess.points,
        });
        const sel = await bitmapSelectionFromMask(res.maskPng, canvas.width, canvas.height);
        if (sel) {
          setSelection(sel);
        } else {
          sess.points.pop(); // a dud point must not poison later refines
          setGenFillError("no subject found at that point");
        }
      } catch (e) {
        // Keep the session (embedding cache makes retry free); drop only
        // the point that failed.
        sess.points.pop();
        throw e;
      }
    } catch (e) {
      setGenFillError(String(e));
    } finally {
      setSubjectBusy(false);
    }
  }

  function textClick(pt: [number, number]) {
    const id = `lyr_${newOpId().slice(3)}`;
    doc.apply({
      op_id: newOpId(),
      type: "add_layer",
      layer: {
        id,
        name: "Text",
        kind: "text",
        text: {
          content: "Text",
          x: Math.round(Math.min(Math.max(pt[0], 0), canvas.width)),
          y: Math.round(Math.min(Math.max(pt[1], 0), canvas.height)),
          size: 64,
          color,
        },
      },
    });
    setActiveLayerId(id);
  }

  const addLayer = () => {
    const id = `lyr_${newOpId().slice(3)}`;
    doc.apply({
      op_id: newOpId(),
      type: "add_layer",
      layer: { id, name: `Layer ${layers.length + 1}`, kind: "paint" },
    });
    setActiveLayerId(id);
  };

  // Flatten the current composite to a PNG File (shared by export, gen-fill
  // source, and promote-to-View). Throws while image layers are loading —
  // masks included: the renderer draws a masked layer only when BOTH are
  // loaded, so a missing mask would silently drop the whole layer from the
  // flatten.
  async function flattenToFile(suffix: string): Promise<File> {
    const notLoaded = (versionId?: string) => versionId && !images.current.get(versionId)?.loaded;
    const stillLoading = doc.state.layers.some(
      (l) => l.kind === "image" && l.visible && (notLoaded(l.versionId) || notLoaded(l.maskVersionId)),
    );
    if (stillLoading) throw new Error("layer images are still loading — try again in a moment");
    await session!.sync.flush();
    const flat = flatten(doc.state, cache, canvas.width, canvas.height);
    const blob = await new Promise<Blob | null>((res) => flat.toBlob(res, "image/png"));
    if (!blob) throw new Error("could not encode PNG");
    return new File([blob], `${canvas.name}${suffix}.png`, { type: "image/png" });
  }

  async function submitGenFill(prompt: string, count: number, ep: GenFillEndpoint) {
    // The genFill guard doubles as a re-entrancy lock: a second submit would
    // overwrite the first job's id — never polled, never cancelable, billed.
    if (!selection || genFill) return;
    // Discard during the submitting phase bumps the nonce; every await below
    // re-checks it so an abandoned submit never creates (or reports) a job.
    const nonce = ++submitNonce.current;
    const aborted = () => submitNonce.current !== nonce;
    setGenFillError(undefined);
    setGenFill({ phase: "submitting" });
    try {
      const profile = pickProfile(ep, canvas.width, canvas.height);
      if (!profile) throw new Error(`canvas exceeds ${ep.name}'s max resolution`);
      // "utility": workflow intermediates — hidden from the default Library.
      const src = await uploadFile(await flattenToFile(" (gen-fill source)"), props.projectId, ["utility"]);
      if (aborted()) return;
      // Removal grows the mask (Photoshop-style): fringes and slight
      // under-selection must not leave amputated object edges behind.
      // Prompted gen-fill keeps the exact selection — it IS the intent.
      const dilate = prompt === "" ? removalDilation(selection) : 0;
      const maskBlob = await renderMaskBlob(selection, canvas.width, canvas.height, dilate);
      const mask = await uploadFile(
        new File([maskBlob], `${canvas.name} (gen-fill mask).png`, { type: "image/png" }),
        props.projectId,
        ["utility"],
      );
      if (aborted()) return;
      const r = await generationClient.createJob({
        job: {
          projectId: props.projectId,
          modelEndpointId: ep.id,
          task: "inpaint",
          profile,
          prompt,
          count,
          targetEntityId: props.canvasId,
          output: { width: canvas.width, height: canvas.height },
          conditioning: {
            sourceImage: { assetId: src.asset!.id, versionId: src.version!.id },
            mask: { assetId: mask.asset!.id, versionId: mask.version!.id },
          },
        },
      });
      if (aborted()) {
        // Job created in the abort window: stop paying for it, silently.
        void generationClient.cancelJob({ id: r.job!.id }).catch(() => {});
        return;
      }
      setGenFill({
        phase: "generating",
        jobId: r.job!.id,
        maskVersionId: mask.version!.id,
        maskAssetId: mask.asset!.id,
        removal: prompt === "",
      });
    } catch (e) {
      if (aborted()) return; // the user already walked away from this submit
      setGenFillError(String(e));
      setGenFill(undefined);
    }
  }

  function commitCandidate() {
    if (genFill?.phase !== "choosing") return;
    const id = `lyr_${newOpId().slice(3)}`;
    doc.apply({
      op_id: newOpId(),
      type: "add_layer",
      layer: {
        id,
        // Removal (empty prompt) and generation read differently in the
        // layers panel — name them apart.
        name: genFill.removal ? "Remove" : "Gen fill",
        kind: "image",
        version_id: genFill.candidates[genFill.index],
        mask_version_id: genFill.maskVersionId,
      },
    });
    setActiveLayerId(id);
    setGenFill(undefined);
    setSelection(undefined);
  }

  async function discardGenFill() {
    const jobId = genFill?.phase === "generating" ? genFill.jobId : undefined;
    // A submit in flight is abandoned by the nonce bump — its remaining
    // awaits see the change and bail (canceling the job if one was created).
    submitNonce.current++;
    // Clear state BEFORE the cancel roundtrip: if the poll fetches CANCELED
    // mid-await, the effect would surface the user's own cancel as an error.
    setGenFill(undefined);
    if (jobId) {
      // Best-effort: stop paying for candidates nobody will look at.
      await generationClient.cancelJob({ id: jobId }).catch(() => {});
    }
  }

  async function promoteToView(sceneId: string) {
    setPromoting(false);
    setExportMsg(undefined);
    try {
      const up = await uploadFile(await flattenToFile(""), props.projectId);
      await storyClient.addView({
        sceneId,
        name: canvas.name,
        plate: { assetId: up.asset!.id, versionId: up.version!.id },
      });
      void qc.invalidateQueries({ queryKey: ["assets"] });
      void qc.invalidateQueries({ queryKey: ["scene"] });
      setExportMsg("Added as view ✓");
    } catch (e) {
      setExportMsg(`Promote failed: ${String(e)}`);
    }
  }

  const doExport = async () => {
    setExporting(true);
    setExportMsg(undefined);
    try {
      await uploadFile(await flattenToFile(""), props.projectId);
      void qc.invalidateQueries({ queryKey: ["assets"] });
      setExportMsg("Exported to Library ✓");
    } catch (e) {
      setExportMsg(`Export failed: ${String(e)}`);
    } finally {
      setExporting(false);
    }
  };

  // The candidate under the arrow keys, rendered as an ephemeral masked
  // layer on top of the composite — comparing happens in place, in context.
  const overlayLayer: LayerState | undefined =
    genFill?.phase === "choosing"
      ? {
          id: `__preview_${genFill.candidates[genFill.index]}`,
          name: "preview",
          kind: "image",
          versionId: genFill.candidates[genFill.index],
          maskVersionId: genFill.maskVersionId,
          opacity: 1,
          visible: true,
          strokes: [],
        }
      : undefined;

  const toolButton = (t: CanvasTool, label: string) => (
    <button className={`btn secondary${tool === t ? " tool-active" : ""}`} onClick={() => setTool(t)}>
      {label}
    </button>
  );

  return (
    <div className="canvas-page">
      <div className="toolbar canvas-toolbar">
        <button
          className="btn secondary"
          onClick={() => {
            // Rejected-and-unretryable ops die with the session — confirm
            // before Back throws them away (Retry save is right there).
            if (
              status === "error" &&
              !retrying &&
              (session?.sync.pendingCount ?? 0) > 0 &&
              !window.confirm("Some edits could not be saved and will be lost if you leave. Leave anyway?")
            ) {
              return;
            }
            props.onBack();
          }}
        >
          ←
        </button>
        <span className="truncate" style={{ maxWidth: 220 }}>
          {canvas.name}
        </span>
        <span className="meta">
          {canvas.width}×{canvas.height}
        </span>
        {toolButton("pan", "✋ Pan")}
        {toolButton("brush", "🖌 Brush")}
        {toolButton("eraser", "◻ Eraser")}
        {toolButton("marquee", "▭ Select")}
        {toolButton("lasso", "◯ Lasso")}
        {toolButton("subject", subjectBusy ? "✨ …" : "✨ Subject")}
        {toolButton("text", "T Text")}
        <input
          type="color"
          value={color}
          onChange={(e) => setColor(e.target.value)}
          title="Brush color"
          aria-label="Brush color"
        />
        <label className="meta">
          size
          <input
            type="range"
            min={1}
            max={200}
            value={brushSize}
            onChange={(e) => setBrushSize(Number(e.target.value))}
          />
        </label>
        <button className="btn secondary" disabled={!doc.canUndo} onClick={() => doc.undo()} title="Cmd+Z">
          ↩
        </button>
        <button
          className="btn secondary"
          disabled={!doc.canRedo}
          onClick={() => doc.redo()}
          title="Shift+Cmd+Z"
        >
          ↪
        </button>
        <span className={`status${status === "error" ? " error" : ""}`}>
          {status === "saved" && "saved"}
          {status === "pending" && "…"}
          {status === "saving" && "saving…"}
          {status === "error" &&
            `save failed: ${syncError ?? ""} ${retrying ? "(retrying)" : "(ops kept locally)"}`}
        </span>
        {status === "error" && !retrying && (
          <button
            className="btn secondary"
            title="Re-attempt the failed save — the edits are still queued locally"
            onClick={() => void session?.sync.flush()}
          >
            ↻ Retry save
          </button>
        )}
        {imageError && <span className="status error">{imageError}</span>}
        {/* The GenFillBar renders errors while mounted; before a selection
            exists (e.g. a failed subject click) this is the only surface. */}
        {genFillError && !selection && !genFill && <span className="status error">{genFillError}</span>}
        <span style={{ flex: 1 }} />
        {exportMsg && <span className="status">{exportMsg}</span>}
        <button className="btn secondary" onClick={() => setPromoting((p) => !p)}>
          → View
        </button>
        <button className="btn" disabled={exporting} onClick={() => void doExport()}>
          {exporting ? "Exporting…" : "Export to Library"}
        </button>
      </div>
      {promoting && <ScenePickerRow projectId={props.projectId} onPick={(id) => void promoteToView(id)} onClose={() => setPromoting(false)} />}
      {(selection || genFill) && (
        <GenFillBar
          endpoints={gfEndpoints}
          docW={canvas.width}
          docH={canvas.height}
          state={genFill}
          progress={gfJob.data?.job?.progress}
          error={genFillError}
          onGenerate={(prompt, count, ep) => void submitGenFill(prompt, count, ep)}
          onPick={(index) => genFill?.phase === "choosing" && setGenFill({ ...genFill, index })}
          onCommit={commitCandidate}
          onDiscard={() => void discardGenFill()}
          onDismiss={() => {
            // Same contract as Esc on the canvas: clear the armed selection
            // (and its subject refine points) so the bar unmounts.
            setSelection(undefined);
            if (subjectRef.current) subjectRef.current.points = [];
          }}
          onUndo={() => session?.doc.undo()}
          onRedo={() => session?.doc.redo()}
        />
      )}

      <div className="canvas-body">
        <CanvasViewport
          doc={doc}
          cache={cache}
          docW={canvas.width}
          docH={canvas.height}
          tool={tool}
          color={color}
          brushSize={brushSize}
          activeLayerId={activeLayerId}
          redrawTick={redrawTick}
          preview={preview}
          selection={selection}
          onSelectionChange={(sel) => {
            setSelection(sel);
            if (subjectRef.current) subjectRef.current.points = [];
          }}
          overlayLayer={overlayLayer}
          onSubjectClick={(pt, neg) => void subjectClick(pt, neg)}
          onTextClick={textClick}
        />
        <aside className="layers-panel">
          <div className="section-head">
            <h3>Layers</h3>
            <button className="btn secondary chip-add" onClick={addLayer}>
              + Layer
            </button>
          </div>
          {[...layers].reverse().map((l) => (
            <div
              key={l.id}
              className={`layer-row${l.id === activeLayerId ? " selected" : ""}`}
              onClick={() => setActiveLayerId(l.id)}
            >
              <button
                className="chip-x"
                title={l.visible ? "Hide" : "Show"}
                onClick={(e) => {
                  e.stopPropagation();
                  doc.apply({
                    op_id: newOpId(),
                    type: "set_layer",
                    layer_id: l.id,
                    props: { visible: !l.visible },
                  });
                }}
              >
                {l.visible ? "👁" : "◡"}
              </button>
              <span className="truncate" style={{ flex: 1 }}>
                {l.name}
                {l.kind === "image" && <span className="meta">{l.maskVersionId ? " · gen-fill" : " · image"}</span>}
              </span>
              <button
                className="chip-x"
                title="Delete layer (undoable)"
                onClick={(e) => {
                  e.stopPropagation();
                  doc.apply({ op_id: newOpId(), type: "remove_layer", layer_id: l.id });
                  if (activeLayerId === l.id) setActiveLayerId(undefined);
                }}
              >
                ×
              </button>
            </div>
          ))}
          {active && (
            <label className="meta layer-opacity">
              opacity {Math.round((preview?.layerId === active.id ? preview.opacity : active.opacity) * 100)}%
              <input
                type="range"
                min={0}
                max={100}
                value={Math.round((preview?.layerId === active.id ? preview.opacity : active.opacity) * 100)}
                onChange={(e) => setPreview({ layerId: active.id, opacity: Number(e.target.value) / 100 })}
                // Commit on every way the gesture can end — a missed
                // pointerup would leave the preview diverged from the doc.
                onPointerUp={commitOpacityPreview}
                onPointerCancel={commitOpacityPreview}
                onLostPointerCapture={commitOpacityPreview}
                onBlur={commitOpacityPreview}
              />
            </label>
          )}
          {active?.kind === "image" && (
            <div className="meta">Image layers are pixels from the Library — paint on a paint layer above.</div>
          )}
          {active?.kind === "text" && active.text && (
            <TextProps
              key={active.id}
              value={active.text}
              onCommit={(text) =>
                doc.apply({ op_id: newOpId(), type: "set_layer", layer_id: active.id, props: { text } })
              }
            />
          )}
        </aside>
      </div>
    </div>
  );
}

/** Text layer props (v0): commit on blur/change-end as single ops — a
 * keystroke-per-op would spam the log and shred undo granularity. */
function TextProps(props: {
  value: { content: string; x: number; y: number; size: number; color: string };
  onCommit: (t: { content: string; x: number; y: number; size: number; color: string }) => void;
}) {
  const [draft, setDraft] = useState(props.value);
  // Resync on external change (undo/remote): a stale draft re-committed on
  // blur would silently revert the user's undo and destroy the redo chain.
  const valueJson = JSON.stringify(props.value);
  useEffect(() => {
    setDraft(props.value);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [valueJson]);
  const commit = () => {
    // Cap defensively too: one >64KB op would poison the whole save queue.
    const next = { ...draft, content: draft.content.slice(0, 10_000) };
    if (JSON.stringify(next) !== valueJson) props.onCommit(next);
  };
  return (
    <div className="layer-opacity">
      <span className="meta">text</span>
      <textarea
        rows={3}
        maxLength={10000}
        value={draft.content}
        onChange={(e) => setDraft({ ...draft, content: e.target.value })}
        onBlur={commit}
      />
      <label className="meta">
        size
        <input
          type="range"
          min={8}
          max={400}
          value={draft.size}
          onChange={(e) => setDraft({ ...draft, size: Number(e.target.value) })}
          onPointerUp={commit}
          onBlur={commit}
        />
      </label>
      <input
        type="color"
        value={draft.color}
        onChange={(e) => setDraft({ ...draft, color: e.target.value })}
        onBlur={commit}
      />
    </div>
  );
}

/** Inline scene picker for promote-to-View (same shape as the Library's). */
function ScenePickerRow(props: { projectId: string; onPick: (sceneId: string) => void; onClose: () => void }) {
  const scenes = useQuery({
    queryKey: ["scenes", props.projectId],
    queryFn: () => storyClient.listScenes({ projectId: props.projectId }),
  });
  return (
    <div className="genfill-bar">
      <span className="meta">Add flattened canvas as a view of…</span>
      {scenes.isLoading && <span className="meta">Loading…</span>}
      {scenes.data?.scenes.length === 0 && <span className="meta">No scenes yet — create one on the Scenes page.</span>}
      {scenes.data?.scenes.map((s) => (
        <button key={s.id} className="btn secondary chip-add" onClick={() => props.onPick(s.id)}>
          {s.name}
        </button>
      ))}
      <button className="chip-x" onClick={props.onClose}>
        ×
      </button>
    </div>
  );
}
