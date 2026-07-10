import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState } from "react";
import type { Timeline } from "@iris/api-client";
import {
  bladeOps,
  clipAt,
  rippleOps,
  MIN_CLIP_S,
  newOpId as oid,
  OpSync,
  type OpSyncTransport,
  snapTime,
  TimelineDoc,
  type TimelineOp,
  timelineDuration,
  type SyncStatus,
} from "@iris/doc-runtime";
import { AudioMixer, ClipDecoder, type Segment } from "@iris/media-engine";
import { assetClient, storyClient, timelineClient, timelineKeepaliveClient } from "../api";
import { EngineCanvas } from "./EngineCanvas";
import { ExportControl } from "./ExportControl";
import { HistoryPanel } from "./HistoryPanel";
import { TranscribeControl } from "./TranscribeControl";
import { AssetKind } from "@iris/api-client";
import { useEscape } from "../components/AssetThumb";

const PX_PER_SEC = 40;
const TRACK_H = 56;
const SNAP_S = 8 / PX_PER_SEC; // 8px feel regardless of zoom (zoom lands later)



const parse = (payloads: string[]): TimelineOp[] =>
  payloads
    .map((p) => {
      try {
        return JSON.parse(p) as TimelineOp;
      } catch {
        return null;
      }
    })
    .filter((o): o is TimelineOp => !!o && !!o.op_id && !!o.type);

export function TimelinePage(props: {
  timelineId: string;
  projectId: string;
  onBack: () => void;
  onGenerateForShot: (shotId: string, label: string) => void;
}) {
  const [session, setSession] = useState<{ tl: Timeline; doc: TimelineDoc; sync: OpSync } | null>(null);
  const [loadError, setLoadError] = useState<string>();
  const [status, setStatus] = useState<SyncStatus>("saved");
  const [, tick] = useState(0);
  const [time, setTime] = useState(0);
  const [picking, setPickingState] = useState<"media" | "shots" | null>(null);
  const pickingRef = useRef<"media" | "shots" | null>(null);
  const setPicking = (v: "media" | "shots" | null) => {
    pickingRef.current = v;
    setPickingState(v);
  };
  const [selected, setSelected] = useState<string | null>(null);
  // Refs mirror playhead/selection for the window keydown handler — reading
  // render state there would either go stale or re-bind the listener per
  // pointermove (the TakePicker lesson).
  const timeRef = useRef(0);
  const selectedRef = useRef<string | null>(null);
  const seek = (t: number) => {
    timeRef.current = t;
    setTime(t);
  };
  const select = (id: string | null) => {
    selectedRef.current = id;
    setSelected(id);
  };
  type DragMode = "move" | "trim-l" | "trim-r";
  const dragRef = useRef<{
    mode: DragMode;
    clipId: string;
    startX: number;
    hasSource: boolean;
    orig: { start: number; duration: number; inPoint: number };
    preview?: { start: number; duration: number; inPoint: number };
  } | null>(null);
  const [dragPreview, setDragPreview] = useState<{ clipId: string; start: number; duration: number } | null>(null);
  const bladeRef = useRef<() => void>(() => {});
  const [playing, setPlaying] = useState(false);
  // Default ON since the audio slice: the engine preview is now the better
  // player (gapless + mixed audio); the <video> chase stays as fallback.
  const [engineOn, setEngineOn] = useState(() => ClipDecoder.supported());
  const [showHistory, setShowHistory] = useState(false);
  const [engineError, setEngineError] = useState<string>();
  // Caption text editing (double-click a caption clip).
  const [editingCap, setEditingCap] = useState<{ id: string; draft: string } | null>(null);
  // Bumped when a transcription completes: server-authored caption ops
  // exist only in the DB — reload the session to pick them up (v1; a live
  // remote-ops channel is the standing sync follow-up).
  const [reloadTick, setReloadTick] = useState(0);
  const playRef = useRef<{ raf: number } | null>(null);
  const togglePlayRef = useRef<() => void>(() => {});

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        let after = 0n;
        let tl: Timeline | undefined;
        const payloads: string[] = [];
        for (;;) {
          const r = await timelineClient.getTimeline({ id: props.timelineId, afterSeq: after });
          tl = r.timeline!;
          for (const op of r.ops) payloads.push(op.payload);
          if (r.ops.length === 0 || r.ops[r.ops.length - 1].seq >= tl.headSeq) break;
          after = r.ops[r.ops.length - 1].seq;
        }
        if (cancelled || !tl) return;
        const doc = new TimelineDoc(parse(payloads));
        const transport: OpSyncTransport = {
          append: async (baseSeq, ps, opts) => {
            const client = opts?.keepalive ? timelineKeepaliveClient : timelineClient;
            const r = await client.appendTimelineOps({
              timelineId: props.timelineId,
              baseSeq: BigInt(baseSeq),
              payloads: ps,
            });
            return Number(r.headSeq);
          },
          fetchSince: async (seq) => {
            // Page to head: a single 2000-op page with the FULL headSeq
            // reported would make a conflict rebase silently skip ops.
            let cursor = BigInt(seq);
            const missed: string[] = [];
            let head = seq;
            for (;;) {
              const r = await timelineClient.getTimeline({ id: props.timelineId, afterSeq: cursor });
              for (const op of r.ops) missed.push(op.payload);
              head = Number(r.timeline!.headSeq);
              if (r.ops.length === 0 || r.ops[r.ops.length - 1].seq >= r.timeline!.headSeq) break;
              cursor = r.ops[r.ops.length - 1].seq;
            }
            return { headSeq: head, payloads: missed };
          },
        };
        const sync = new OpSync(transport, Number(tl.headSeq));
        doc.onLocalOp = (op) => sync.enqueue(op as never);
        sync.onRemoteOps = (ps) => doc.applyRemote(parse(ps));
        sync.onStatus = (s) => setStatus(s);
        setSession({ tl, doc, sync });
      } catch (e) {
        if (!cancelled) setLoadError(String(e));
      }
    })();
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [props.timelineId, reloadTick]);

  useEffect(() => {
    if (!session) return;
    const unsub = session.doc.subscribe(() => tick((t) => t + 1));
    const flush = () => void session.sync.flush(true);
    window.addEventListener("beforeunload", flush);
    document.addEventListener("visibilitychange", flush);
    return () => {
      unsub();
      window.removeEventListener("beforeunload", flush);
      document.removeEventListener("visibilitychange", flush);
      flush();
    };
  }, [session]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement;
      // <select> type-ahead ("b"…) and sibling panels' form fields must
      // never reach doc-mutating hotkeys.
      if (t.isContentEditable || t.closest?.("input,textarea,select,[contenteditable],.panel")) return;
      // No doc mutation while a picker modal is open (the edit would land
      // invisibly behind the overlay — and Escape belongs to the modal) or
      // mid-drag (the pending gesture would commit from a stale orig).
      if (pickingRef.current || dragRef.current) return;
      // App-shell overlays (⌘K palette, ? help) sit outside this page's
      // component tree — Space/B/Delete must read them, not the doc.
      // Deliberately NOT a blanket .overlay check: page-owned modals
      // (ClipPlayer, pickers) already gate via pickingRef.
      if (document.querySelector(".palette-overlay, .help-overlay")) return;
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "z") {
        e.preventDefault();
        e.shiftKey ? session?.doc.redo() : session?.doc.undo();
        return;
      }
      if (e.metaKey || e.ctrlKey || e.altKey) return;
      if (e.key === " ") {
        // Cancel default BEFORE the repeat check: uncancelled repeats
        // scroll the page and re-arm focused-button click-on-keyup.
        e.preventDefault();
        if (e.repeat) return;
        togglePlayRef.current?.();
        return;
      }
      if (e.key === "b" || e.key === "B") bladeRef.current?.();
      else if (e.key === "Delete" || e.key === "Backspace") {
        const id = selectedRef.current;
        if (id && session) {
          removeClipRef.current?.(id, e.shiftKey);
          select(null);
        }
      } else if (e.key === "Escape") select(null);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [session]);

  // Playback v1: a wall-clock rAF loop drives the playhead; the preview
  // video chases it (drift-corrected). Uniform across media clips, shot
  // placeholders and gaps — frame-accurate compositing arrives with the
  // WebCodecs engine.
  const stopPlay = () => {
    if (playRef.current) cancelAnimationFrame(playRef.current.raf);
    playRef.current = null;
    setPlaying(false);
  };
  const startPlay = () => {
    const pdoc = session?.doc;
    if (playRef.current || !pdoc) return;
    const end0 = timelineDuration(pdoc.state);
    if (end0 <= 0) return;
    // At the end, space restarts from the top (player muscle memory).
    const t0 = timeRef.current >= end0 - 0.01 ? 0 : timeRef.current;
    // Commit t0 NOW: the mixer effect reads timeRef before the first rAF
    // tick, and a restart-from-end would otherwise schedule audio from the
    // OLD playhead (= end → empty plan → silent replay). Also snaps the
    // playhead UI immediately instead of on the first tick.
    seek(t0);
    const start = performance.now();
    setPlaying(true);
    const step = () => {
      const t = t0 + (performance.now() - start) / 1000;
      const end = timelineDuration(pdoc.state); // re-read: edits mid-play move the end
      if (t >= end) {
        seek(end);
        stopPlay();
        return;
      }
      seek(t);
      playRef.current = { raf: requestAnimationFrame(step) };
    };
    playRef.current = { raf: requestAnimationFrame(step) };
  };
  const togglePlay = () => (playRef.current ? stopPlay() : startPlay());
  useEffect(() => {
    togglePlayRef.current = togglePlay;
  });
  useEffect(() => {
    // rAF freezes in hidden tabs but the unmuted <video> keeps playing —
    // audio would run past the clip's out-point with the playhead frozen.
    const onVis = () => {
      if (document.hidden) stopPlay();
    };
    document.addEventListener("visibilitychange", onVis);
    return () => {
      document.removeEventListener("visibilitychange", onVis);
      stopPlay(); // unmount: kill the loop
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Remove a clip; with ripple=true the removed span closes (clips after
  // its end shift left by its duration — same track only). Multi-op like
  // blade: one undo per op.
  const removeClip = (clipId: string, ripple: boolean) => {
    const rdoc = session?.doc;
    if (!rdoc) return;
    const st = rdoc.state;
    const track = st.tracks.find((t) => t.clips.some((c) => c.id === clipId));
    const clip = track?.clips.find((c) => c.id === clipId);
    rdoc.apply({ op_id: oid(), type: "remove_clip", clip_id: clipId });
    if (ripple && track && clip) {
      for (const op of rippleOps(rdoc.state, track.id, clip.start + clip.duration, -clip.duration)) {
        rdoc.apply(op);
      }
    }
  };
  const removeClipRef = useRef<(clipId: string, ripple: boolean) => void>(() => {});
  useEffect(() => {
    removeClipRef.current = removeClip;
  });

  const blade = () => {
    const bdoc = session?.doc;
    if (!bdoc) return;
    const st = bdoc.state;
    const t = timeRef.current;
    const inSel = (id: string | null) => {
      if (!id) return undefined;
      for (const tr of st.tracks) {
        const c = tr.clips.find((c) => c.id === id);
        if (c && t > c.start && t < c.start + c.duration) return c;
      }
      return undefined;
    };
    const target = inSel(selectedRef.current) ?? clipAt(st, t, "video") ?? clipAt(st, t, "audio");
    if (!target) return;
    // Resolved shot clips have a real source: the right half continues the
    // take instead of restarting it (via the ref — blade is declared above
    // the resolution hooks).
    const hasSource =
      !!target.versionId || !!(target.shotId && takeByShotRef.current.get(target.shotId));
    const ops = bladeOps(st, target.id, t, `cl_${oid().slice(3)}`, hasSource);
    if (ops) for (const op of ops) bdoc.apply(op);
  };
  // Assigned in an effect, not during render: a discarded concurrent render
  // must not leave its closure behind. Declared before the early returns so
  // the effect never sees an uninitialized binding on loading renders.
  useEffect(() => {
    bladeRef.current = blade;
  });

  // Engine segments (hooks — must run every render, so they live above the
  // early returns and read session optionally): every video clip resolved
  // to a version (media clips directly; shot clips via their selected
  // take). Unresolved spans stay out — the placeholder overlay covers them.
  const qc = useQueryClient();
  const docState = session?.doc.state;
  const videoClips = useMemo(
    () => (docState?.tracks ?? []).filter((t) => t.kind === "video").flatMap((t) => t.clips),
    [docState],
  );
  const shotIds = useMemo(
    () => [...new Set(videoClips.filter((c) => !c.versionId && c.shotId).map((c) => c.shotId!))],
    [videoClips],
  );
  const shotQueries = useQueries({
    queries: shotIds.map((id) => ({
      queryKey: ["shot", id],
      queryFn: () => storyClient.getShot({ id }),
      staleTime: 30_000,
      // NOT gated on the engine toggle: resolution knowledge also drives
      // GESTURE semantics (source-aware blade/trim). A blade in <video>
      // fallback mode must not persist in_point 0 for a resolved shot —
      // that writes duplicated-content halves into the op log.
    })),
  });
  const takeKey = shotQueries
    .map((q) => `${q.data?.shot?.selectedTakeVersionId ?? (q.isPending ? "?" : "")}:${q.data?.shot?.selectedTakeContentType ?? ""}`)
    .join(",");
  // shotId → resolved take {versionId, contentType}. Consumed by segments,
  // gestures (source-aware blade/trim on resolved shots) and the image-
  // take overlay.
  const takeByShot = useMemo(
    () =>
      new Map(
        shotIds.map((id, i) => [
          id,
          shotQueries[i].data?.shot?.selectedTakeVersionId
            ? {
                versionId: shotQueries[i].data!.shot!.selectedTakeVersionId,
                contentType: shotQueries[i].data!.shot!.selectedTakeContentType || "video/mp4",
              }
            : undefined,
        ]),
      ),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [shotIds, takeKey],
  );
  const takeByShotRef = useRef(takeByShot);
  useEffect(() => {
    takeByShotRef.current = takeByShot;
  });
  // Stable identity while nothing resolved changes: the EngineCanvas
  // effect keys on this array, and a fresh array per render would spawn
  // paused re-decodes on every drag-preview / autosave status tick.
  const segments: Segment[] = useMemo(() => {
    return videoClips.flatMap((c) => {
      const take = c.shotId ? takeByShot.get(c.shotId) : undefined;
      // Image takes render as stills (overlay), never as decode segments.
      const sourceId = c.versionId ?? (take?.contentType.startsWith("video/") ? take.versionId : undefined);
      return sourceId ? [{ sourceId, startS: c.start, durationS: c.duration, inPointS: c.inPoint }] : [];
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [videoClips, takeByShot]);
  const shotSettled = useMemo(
    () => new Map(shotIds.map((id, i) => [id, !shotQueries[i].isPending])),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [shotIds, takeKey],
  );
  const srcFor = (versionId: string) =>
    qc.fetchQuery({
      queryKey: ["previewSrc", versionId],
      staleTime: 5 * 60_000,
      retry: false, // parity with PreviewPane on the shared key — fail fast into the engine's error surface
      queryFn: async () => {
        try {
          return (await assetClient.signDownload({ versionId, variant: "proxy" })).url;
        } catch {
          return (await assetClient.signDownload({ versionId })).url;
        }
      },
    });
  // Audible spans: video segments carry their sources' embedded audio;
  // audio-track clips add music/VO. Overlaps MIX (NLE semantics — every
  // unmuted track sounds); per-clip gain lands with the mixer UI.
  const audioSegments: Segment[] = useMemo(() => {
    const audioClips = (docState?.tracks ?? [])
      .filter((t) => t.kind === "audio")
      .flatMap((t) => t.clips)
      .flatMap((c) =>
        c.versionId ? [{ sourceId: c.versionId, startS: c.start, durationS: c.duration, inPointS: c.inPoint }] : [],
      );
    return [...segments, ...audioClips];
  }, [segments, docState]);
  const mixerRef = useRef<AudioMixer | null>(null);
  const audioSegmentsRef = useRef(audioSegments);
  useEffect(() => {
    audioSegmentsRef.current = audioSegments;
  });
  useEffect(() => {
    if (playing && engineOn && AudioMixer.supported()) {
      mixerRef.current ??= new AudioMixer(srcFor, () =>
        setEngineError("🔇 audio blocked by the browser — click ▶ once to unlock"),
      );
      // Scheduled once per play from the playhead; mid-play edits reschedule
      // on the next play (the video side re-reads state live — recorded
      // asymmetry until the mixer follows doc changes).
      mixerRef.current.play(audioSegmentsRef.current, timeRef.current);
    } else {
      mixerRef.current?.stop();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [playing, engineOn]);
  useEffect(
    () => () => {
      mixerRef.current?.dispose();
    },
    [],
  );

  if (loadError) return <div className="status error">Couldn’t open timeline: {loadError}</div>;
  if (!session) return <div className="empty">Opening timeline…</div>;
  const { doc, tl } = session;
  const state = doc.state;
  const dur = Math.max(timelineDuration(state), 30);
  const active = clipAt(state, time, "video");
  const activeCaption = clipAt(state, time, "caption");

  // Capture keeps drags alive off-element but can throw for already-
  // released pointers — never let it kill the gesture (state first).
  const capture = (e: React.PointerEvent) => {
    try {
      e.currentTarget.setPointerCapture(e.pointerId);
    } catch {
      /* uncaptured drag still works over the element */
    }
  };
  const onClipDown = (
    e: React.PointerEvent,
    mode: DragMode,
    c: { id: string; start: number; duration: number; inPoint: number; versionId?: string; shotId?: string },
  ) => {
    select(c.id);
    if (e.button !== 0) return; // right-click opens the context menu, which can swallow pointerup
    dragRef.current = {
      mode,
      clipId: c.id,
      startX: e.clientX,
      // A resolved shot clip trims like media: its selected take is a real
      // source (in-point slip + no-reveal-before-0 clamp apply).
      hasSource: !!c.versionId || !!(c.shotId && takeByShotRef.current.get(c.shotId)),
      orig: { start: c.start, duration: c.duration, inPoint: c.inPoint },
    };
    capture(e);
  };
  const onClipMove = (e: React.PointerEvent) => {
    const d = dragRef.current;
    if (!d) return;
    if (e.buttons === 0) {
      // The gesture's pointerup landed elsewhere (failed capture, clip
      // unmounted by a remote op) — a hover must not drag a phantom.
      dragRef.current = null;
      setDragPreview(null);
      return;
    }
    const dx = (e.clientX - d.startX) / PX_PER_SEC;
    // Alt disables snapping (NLE convention); the playhead is a snap target.
    // doc.state/timeRef, not render scope — the refs-not-render-state rule.
    const snap = (t: number) =>
      e.altKey ? null : snapTime(doc.state, t, { thresholdS: SNAP_S, excludeClipId: d.clipId, extra: [timeRef.current, 0] });
    let p = d.preview ?? d.orig;
    if (d.mode === "move") {
      const raw = Math.max(0, d.orig.start + dx);
      // Snap whichever clip edge found a target; both hit → smaller distance.
      const byLead = snap(raw);
      const trailHit = snap(raw + d.orig.duration);
      const byTrail = trailHit === null ? null : trailHit - d.orig.duration;
      let lead = byLead ?? byTrail ?? raw;
      if (byLead !== null && byTrail !== null && Math.abs(byTrail - raw) < Math.abs(byLead - raw)) lead = byTrail;
      p = { start: Math.max(0, lead), duration: d.orig.duration, inPoint: d.orig.inPoint };
    } else if (d.mode === "trim-r") {
      // No far cap: the model carries no source duration, so a media clip
      // can extend past its source (preview freezes on the last frame);
      // render policy for that overhang is deferred with the compositor.
      const rawEnd = d.orig.start + Math.max(MIN_CLIP_S, d.orig.duration + dx);
      const end = snap(rawEnd) ?? rawEnd;
      p = { ...d.orig, duration: Math.max(MIN_CLIP_S, end - d.orig.start) };
    } else {
      // trim-l slips the in-point so content stays anchored; clamped so no
      // source before 0 is revealed and at least one frame remains. Clips
      // without a source (shot placeholders) have nothing to anchor: they
      // extend freely to the timeline origin with in_point pinned at 0.
      const lo = d.hasSource ? d.orig.start - d.orig.inPoint : 0;
      const hi = d.orig.start + d.orig.duration - MIN_CLIP_S;
      const rawNs = d.orig.start + dx;
      const ns = Math.min(hi, Math.max(Math.max(0, lo), snap(rawNs) ?? rawNs));
      const delta = ns - d.orig.start;
      p = { start: ns, duration: d.orig.duration - delta, inPoint: d.hasSource ? d.orig.inPoint + delta : 0 };
    }
    // The ref is the source of truth for the drop — dragPreview state can be
    // a frame stale on a fast release (continuous-priority updates).
    d.preview = p;
    setDragPreview({ clipId: d.clipId, start: p.start, duration: p.duration });
  };
  const r2 = (n: number) => Math.round(n * 100) / 100;
  const onClipUp = (e?: React.PointerEvent) => {
    const d = dragRef.current;
    dragRef.current = null;
    setDragPreview(null);
    if (!d?.preview) return;
    const { mode, clipId, orig, preview: p } = d;
    if (mode === "move" && Math.abs(p.start - orig.start) > 0.01) {
      doc.apply({ op_id: oid(), type: "move_clip", clip_id: clipId, start: r2(p.start) });
    } else if (mode === "trim-r" && Math.abs(p.duration - orig.duration) > 0.01) {
      doc.apply({ op_id: oid(), type: "trim_clip", clip_id: clipId, duration: r2(p.duration) });
      // ⇧ on release = ripple: everything after the ORIGINAL out-point
      // follows the edge (delta > 0 pushes later clips out, < 0 pulls in).
      if (e?.shiftKey) {
        const track = doc.state.tracks.find((t) => t.clips.some((c) => c.id === clipId));
        if (track) {
          const delta = r2(p.duration) - orig.duration;
          for (const op of rippleOps(doc.state, track.id, orig.start + orig.duration, delta)) {
            doc.apply(op);
          }
        }
      }
    } else if (mode === "trim-l" && Math.abs(p.start - orig.start) > 0.01) {
      doc.apply({
        op_id: oid(),
        type: "trim_clip",
        clip_id: clipId,
        start: r2(p.start),
        duration: r2(p.duration),
        in_point: r2(p.inPoint),
      });
    }
  };

  return (
    <div>
      <div className="toolbar">
        <button className="btn secondary" onClick={props.onBack}>←</button>
        <span className="truncate" style={{ maxWidth: 220 }}>{tl.name}</span>
        <span className="meta">{tl.fps} fps</span>
        <button className="btn secondary" onClick={() => setPicking("media")}>+ Clip</button>
        <button className="btn secondary" onClick={() => setPicking("shots")}>⧉ Scene shots</button>
        <button className="btn secondary" disabled={!doc.canUndo} onClick={() => doc.undo()}>↩</button>
        <button className="btn secondary" disabled={!doc.canRedo} onClick={() => doc.redo()}>↪</button>
        <button className="btn secondary" title="Blade at playhead (B)" onClick={blade}>🔪</button>
        <button className="btn secondary" title="Play/pause (space)" onClick={togglePlay}>{playing ? "⏸" : "▶"}</button>
        <button
          className={`btn secondary${showHistory ? " tool-active" : ""}`}
          title="History — the op log, newest first"
          onClick={() => setShowHistory((v) => !v)}
        >
          🕘
        </button>
        {ClipDecoder.supported() && (
          <button
            className={`btn secondary${engineOn ? " tool-active" : ""}`}
            title="WebCodecs compositor preview (default) — gapless boundaries + mixed audio; toggle for the <video> fallback"
            onClick={() => {
              setEngineError(undefined);
              setEngineOn((v) => !v);
            }}
          >
            ⚙
          </button>
        )}
        <TranscribeControl
          timelineId={props.timelineId}
          onComplete={() => {
            // Server-authored ops are only in the DB: flush local edits,
            // then rebuild the session over the fresh log.
            void session?.sync.flush(true).then(() => setReloadTick((t) => t + 1));
          }}
        />
        <ExportControl timelineId={props.timelineId} />
        <span className={`status${status === "error" ? " error" : ""}`}>{status === "saved" ? "saved" : status === "error" ? "save failed — ops kept locally" : "…"}</span>
      </div>

      {showHistory && <HistoryPanel doc={doc} onClose={() => setShowHistory(false)} />}
      {engineOn ? (
        <div className="tl-preview">
          <EngineCanvas segments={segments} time={time} playing={playing} srcFor={srcFor} onError={setEngineError} />
          {engineError && <div className="tl-preview-note">{engineError}</div>}
          {active?.shotId && takeByShot.get(active.shotId)?.contentType.startsWith("image/") && (
            <StillOverlay versionId={takeByShot.get(active.shotId)!.versionId} />
          )}
          {active?.shotId &&
            shotSettled.get(active.shotId) !== false &&
            !takeByShot.get(active.shotId) &&
            !segments.some((sg) => time >= sg.startS && time < sg.startS + sg.durationS) && (
              <div className="tl-preview-shot tl-preview-overlay">
                <div className="meta">🎬 {active.name} — no take selected</div>
                <button className="btn" onClick={() => props.onGenerateForShot(active.shotId!, active.name)}>
                  ⚡ Generate into slot
                </button>
              </div>
            )}
          {!active && <div className="meta tl-preview-overlay">No clip under the playhead.</div>}
          {activeCaption?.text && <div className="tl-caption-render">{activeCaption.text}</div>}
        </div>
      ) : (
        <div className="tl-preview-wrap">
          <PreviewPane clip={active} time={time} playing={playing} onGenerate={props.onGenerateForShot} />
          {activeCaption?.text && <div className="tl-caption-render">{activeCaption.text}</div>}
        </div>
      )}

      <div
        className="tl-ruler"
        style={{ width: dur * PX_PER_SEC }}
        onPointerDown={(e) => {
          stopPlay();
          const r = e.currentTarget.getBoundingClientRect();
          seek(Math.max(0, (e.clientX - r.left) / PX_PER_SEC));
          capture(e);
        }}
        onPointerMove={(e) => {
          if (e.buttons !== 1) return;
          const r = e.currentTarget.getBoundingClientRect();
          seek(Math.max(0, (e.clientX - r.left) / PX_PER_SEC));
        }}
      >
        {Array.from({ length: Math.ceil(dur) + 1 }, (_, i) => (
          <span key={i} className="tl-tick" style={{ left: i * PX_PER_SEC }}>{i}</span>
        ))}
        <div className="tl-playhead" style={{ left: time * PX_PER_SEC }} />
      </div>

      <div className="tl-tracks" style={{ width: dur * PX_PER_SEC }}>
        {state.tracks.map((track) => (
          <div key={track.id} className={`tl-track tl-${track.kind}`} style={{ height: TRACK_H }}>
            <span className="tl-track-label">{track.name}</span>
            {track.clips.map((c) => {
              const preview = dragPreview?.clipId === c.id ? dragPreview : undefined;
              const start = preview?.start ?? c.start;
              const width = (preview?.duration ?? c.duration) * PX_PER_SEC;
              const handle = (mode: DragMode, cls: string) => (
                <div
                  className={`tl-handle ${cls}`}
                  onPointerDown={(e) => {
                    e.stopPropagation();
                    onClipDown(e, mode, c);
                  }}
                />
              );
              return (
                <div
                  key={c.id}
                  className={`tl-clip${c.shotId ? " tl-shot" : ""}${track.kind === "caption" ? " tl-cap" : ""}${active?.id === c.id ? " tl-active" : ""}${selected === c.id ? " tl-selected" : ""}`}
                  style={{ left: start * PX_PER_SEC, width }}
                  onPointerDown={(e) => onClipDown(e, "move", c)}
                  onPointerMove={onClipMove}
                  onPointerUp={onClipUp}
                  onPointerCancel={() => onClipUp() /* a cancelled gesture commits the trim (pre-existing) but must never amplify into a ripple */}
                  onDoubleClick={() => {
                    if (track.kind === "caption") setEditingCap({ id: c.id, draft: c.text ?? "" });
                  }}
                  title={
                    track.kind === "caption"
                      ? `${c.text ?? ""} — double-click edits · drag moves · edges trim`
                      : `${c.name} · ${c.duration.toFixed(1)}s — drag moves · edges trim (⇧ right edge: ripple)`
                  }
                >
                  {handle("trim-l", "tl-handle-l")}
                  {editingCap?.id === c.id ? (
                    <input
                      className="tl-cap-input"
                      autoFocus
                      value={editingCap.draft}
                      onPointerDown={(e) => e.stopPropagation()}
                      onChange={(e) => setEditingCap({ id: c.id, draft: e.target.value })}
                      onBlur={() => {
                        if (editingCap.draft !== (c.text ?? ""))
                          doc.apply({ op_id: oid(), type: "set_clip_text", clip_id: c.id, text: editingCap.draft });
                        setEditingCap(null);
                      }}
                      onKeyDown={(e) => {
                        e.stopPropagation(); // Space/B/Delete belong to the text, not the doc
                        if (e.key === "Enter") (e.target as HTMLInputElement).blur();
                        if (e.key === "Escape") setEditingCap(null);
                      }}
                    />
                  ) : (
                    <span className="truncate">
                      {track.kind === "caption" ? `💬 ${c.text ?? ""}` : c.shotId ? `🎬 ${c.name}` : c.name}
                    </span>
                  )}
                  <button
                    className="chip-x"
                    title="Remove (⇧: ripple — close the gap)"
                    onPointerDown={(e) => e.stopPropagation()}
                    onClick={(e) => removeClip(c.id, e.shiftKey)}
                  >
                    ×
                  </button>
                  {handle("trim-r", "tl-handle-r")}
                </div>
              );
            })}
            <div className="tl-playhead" style={{ left: time * PX_PER_SEC }} />
          </div>
        ))}
      </div>

      {picking === "media" && (
        <MediaPicker
          projectId={props.projectId}
          onPick={async (assetId, name) => {
            setPicking(null);
            const { asset, versions } = await assetClient.getAsset({ id: assetId });
            const head = versions.find((v) => v.id === asset?.headVersionId) ?? versions[0];
            if (!head) return;
            const kind = head.contentType.startsWith("audio/") ? "audio" : "video";
            let track = state.tracks.find((t) => t.kind === kind);
            if (!track) {
              // Self-heal docs without a matching track (e.g. seeded before
              // this build) instead of silently dropping the add.
              const id = `trk_${oid().slice(3)}`;
              doc.apply({ op_id: oid(), type: "add_track", track: { id, kind, name: kind === "video" ? "V1" : "A1" } });
              track = doc.state.tracks.find((t) => t.id === id)!;
            }
            doc.apply({
              op_id: oid(),
              type: "add_clip",
              track_id: track.id,
              clip: { id: `cl_${oid().slice(3)}`, name, version_id: head.id, start: time, duration: head.durationS || 5 },
            });
          }}
          onClose={() => setPicking(null)}
        />
      )}
      {picking === "shots" && (
        <ShotPicker
          projectId={props.projectId}
          onPick={(shots) => {
            setPicking(null);
            let track = state.tracks.find((t) => t.kind === "video");
            if (!track) {
              const id = `trk_${oid().slice(3)}`;
              doc.apply({ op_id: oid(), type: "add_track", track: { id, kind: "video", name: "V1" } });
              track = doc.state.tracks.find((t) => t.id === id)!;
            }
            // Placeholder clips laid end-to-end from the current timeline end.
            let cursor = timelineDuration(state);
            for (const s of shots) {
              doc.apply({
                op_id: oid(),
                type: "add_clip",
                track_id: track.id,
                clip: { id: `cl_${oid().slice(3)}`, name: s.name, shot_id: s.id, start: cursor, duration: s.duration || 5 },
              });
              cursor += s.duration || 5;
            }
          }}
          onClose={() => setPicking(null)}
        />
      )}
    </div>
  );
}

/** Image-take spans in engine mode: the take is a still — paint it above
 * the (blank) canvas for its whole duration. Owns its queryFn: wrapping
 * srcFor (a fetchQuery on the SAME key) would await its own in-flight
 * promise and never resolve. */
function StillOverlay(props: { versionId: string }) {
  const still = useQuery({
    queryKey: ["previewSrc", props.versionId],
    staleTime: 5 * 60_000,
    retry: false,
    queryFn: async () => {
      try {
        return (await assetClient.signDownload({ versionId: props.versionId, variant: "proxy" })).url;
      } catch {
        return (await assetClient.signDownload({ versionId: props.versionId })).url;
      }
    },
  });
  const qc = useQueryClient();
  return still.data ? (
    <img
      className="tl-still"
      src={still.data}
      alt=""
      onError={() => void qc.invalidateQueries({ queryKey: ["previewSrc", props.versionId] })}
    />
  ) : null;
}

/** Clip under the playhead. Media clips play/seek the proxy; shot clips
 * resolve their selected take (GetShot → selected_take_version_id) and play
 * it like media, falling back to the generate-into-slot placeholder. While
 * playing, the wall-clock playhead leads and the video chases it — paused,
 * every seek is exact. Only the active clip's own audio plays; audio-track
 * mixing arrives with the WebCodecs compositor. */
const DRIFT_S = 0.15; // correct the chase only past ~4 frames — resets stutter

function PreviewPane(props: {
  clip?: { id: string; name: string; versionId?: string; shotId?: string; start: number; inPoint: number };
  time: number;
  playing: boolean;
  onGenerate: (shotId: string, label: string) => void;
}) {
  const videoRef = useRef<HTMLVideoElement>(null);
  const qc = useQueryClient();
  // Safari/Firefox can reject unmuted play() from effects (activation is
  // per-element there, and clip switches mint new elements) — retry muted
  // so the picture keeps running, and say so instead of freezing silently.
  const [audioBlocked, setAudioBlocked] = useState(false);
  const [mediaError, setMediaError] = useState(false);
  const tryPlay = (v: HTMLVideoElement) => {
    v.play().catch(() => {
      v.muted = true;
      setAudioBlocked(true);
      v.play().catch(() => {});
    });
  };
  const c = props.clip;

  const shotQ = useQuery({
    queryKey: ["shot", c?.shotId],
    enabled: !!c?.shotId,
    staleTime: 30_000,
    queryFn: () => storyClient.getShot({ id: c!.shotId! }),
  });
  const versionId = c?.versionId || shotQ.data?.shot?.selectedTakeVersionId || undefined;
  const isImageTake = !c?.versionId && (shotQ.data?.shot?.selectedTakeContentType ?? "").startsWith("image/");

  const srcQ = useQuery({
    queryKey: ["previewSrc", versionId],
    enabled: !!versionId,
    staleTime: 5 * 60_000, // presigned URLs outlive this comfortably
    retry: false,
    queryFn: async () => {
      try {
        return (await assetClient.signDownload({ versionId: versionId!, variant: "proxy" })).url;
      } catch {
        return (await assetClient.signDownload({ versionId: versionId! })).url;
      }
    },
  });
  const src = versionId ? srcQ.data : undefined;

  const want = c ? Math.max(0, props.time - c.start + c.inPoint) : 0;
  useEffect(() => {
    const v = videoRef.current;
    if (!v || !c || Number.isNaN(v.duration)) return;
    if (props.playing) {
      if (Math.abs(v.currentTime - want) > DRIFT_S) v.currentTime = want;
      if (v.paused) tryPlay(v);
    } else {
      if (!v.paused) v.pause();
      v.currentTime = want;
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [props.time, props.playing, c, want]);
  useEffect(() => {
    if (!props.playing) setAudioBlocked(false);
  }, [props.playing]);
  useEffect(() => {
    setMediaError(false);
  }, [versionId, srcQ.data]);

  return (
    <div className="tl-preview">
      {src && isImageTake && (
        <img
          className="tl-still"
          src={src}
          alt=""
          onError={() => void qc.invalidateQueries({ queryKey: ["previewSrc", versionId] })}
        />
      )}
      {src && !isImageTake && (
        <video
          key={versionId /* source switch = fresh element; a re-signed URL for the SAME version must not remount (black flash on refocus) */}
          ref={videoRef}
          src={src}
          muted={!props.playing}
          playsInline
          onLoadedMetadata={(e) => {
            // First seek can arrive before metadata; re-apply once known.
            e.currentTarget.currentTime = want;
            if (props.playing) tryPlay(e.currentTarget);
          }}
          onError={() => {
            // Likely an expired presign (15 min): re-sign and let the new
            // URL flow into src — self-healing, but say what happened.
            setMediaError(true);
            void qc.invalidateQueries({ queryKey: ["previewSrc", versionId] });
          }}
        />
      )}
      {(srcQ.isError || mediaError) && (
        <div className="meta">Preview unavailable — {mediaError ? "the signed link may have expired; retrying" : "signing failed"}.</div>
      )}
      {shotQ.isError && <div className="meta">Preview unavailable — shot lookup failed.</div>}
      {audioBlocked && props.playing && <div className="tl-preview-note">🔇 audio blocked by the browser — muted playback</div>}
      {c?.shotId && !versionId && !shotQ.isPending && !shotQ.isError && (
        <div className="tl-preview-shot">
          <div className="meta">🎬 {c.name} — no take selected</div>
          <button className="btn" onClick={() => props.onGenerate(c.shotId!, c.name)}>
            ⚡ Generate into slot
          </button>
        </div>
      )}
      {!c && <div className="meta">No clip under the playhead.</div>}
    </div>
  );
}

function MediaPicker(props: { projectId: string; onPick: (assetId: string, name: string) => void; onClose: () => void }) {
  useEscape(props.onClose);
  const assets = useQuery({
    queryKey: ["assets", props.projectId],
    queryFn: () => assetClient.listAssets({ projectId: props.projectId }),
  });
  const media = (assets.data?.assets ?? []).filter((a) => a.kind === AssetKind.VIDEO || a.kind === AssetKind.AUDIO);
  return (
    <div className="overlay" onClick={props.onClose}>
      <div className="modal" role="dialog" onClick={(e) => e.stopPropagation()}>
        <div className="panel-header">
          <h3>Add clip</h3>
          <button className="btn secondary" onClick={props.onClose}>Close</button>
        </div>
        {media.length === 0 && <div className="empty">No video/audio in the Library yet.</div>}
        <div className="promote-list">
          {media.map((a) => (
            <button key={a.id} className="btn secondary chip-add" onClick={() => props.onPick(a.id, a.name)}>
              {a.kind === AssetKind.AUDIO ? "🎵" : "🎬"} <span className="truncate">{a.name}</span>
            </button>
          ))}
        </div>
      </div>
    </div>
  );
}

function ShotPicker(props: {
  projectId: string;
  onPick: (shots: { id: string; name: string; duration: number }[]) => void;
  onClose: () => void;
}) {
  useEscape(props.onClose);
  const qc = useQueryClient();
  const scenes = useQuery({
    queryKey: ["scenes", props.projectId],
    queryFn: () => storyClient.listScenes({ projectId: props.projectId }),
  });
  const pick = async (sceneId: string) => {
    const r = await qc.fetchQuery({
      queryKey: ["scene", sceneId],
      queryFn: () => storyClient.getScene({ id: sceneId }),
    });
    const shots = (r.scene?.shots ?? []).map((s, i) => ({
      id: s.id,
      name: s.description || `Shot ${i + 1}`,
      duration: s.durationTargetS || 5,
    }));
    props.onPick(shots);
  };
  return (
    <div className="overlay" onClick={props.onClose}>
      <div className="modal" role="dialog" onClick={(e) => e.stopPropagation()}>
        <div className="panel-header">
          <h3>Add scene shots as placeholder clips</h3>
          <button className="btn secondary" onClick={props.onClose}>Close</button>
        </div>
        {scenes.data?.scenes.length === 0 && <div className="empty">No scenes yet.</div>}
        <div className="promote-list">
          {scenes.data?.scenes.map((s) => (
            <button key={s.id} className="btn secondary chip-add" onClick={() => void pick(s.id)}>
              {s.name}
            </button>
          ))}
        </div>
      </div>
    </div>
  );
}
