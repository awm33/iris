import { useEffect, useRef, useState } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import { assetClient } from "../api";
import { useEscape } from "./AssetThumb";

// Clip player (M5 slice 2): native <video> over the prep proxy — the
// profiling-not-ideology choice for a single clip. The WebCodecs decode
// pipeline debuts with the timeline compositor, where per-frame control
// over multiple clips is unavoidable (same pattern as the doc runtime).
//
// Transport follows NLE muscle memory: J/K/L (reverse isn't native to
// <video>; J steps backward repeatedly at 1/4-second granularity), space
// toggles, ←/→ step one frame (1/24s default), I/O display only (marks
// land with the timeline).

async function signVariant(versionId: string, variant: string): Promise<string | null> {
  try {
    const r = await assetClient.signDownload({ versionId, variant });
    return r.url;
  } catch (e) {
    if (e instanceof ConnectError && e.code === Code.NotFound) return null;
    throw e;
  }
}

export function ClipPlayer(props: { versionId: string; title?: string; onClose: () => void }) {
  useEscape(props.onClose);
  const videoRef = useRef<HTMLVideoElement>(null);
  const [error, setError] = useState<string>();
  const [usingProxy, setUsingProxy] = useState<boolean>();
  const [strip, setStrip] = useState<{ url: string; cols: number } | null>(null);
  const [waveform, setWaveform] = useState<number[] | null>(null);
  const [time, setTime] = useState(0);
  const [duration, setDuration] = useState(0);
  const [playing, setPlaying] = useState(false);
  const jogRef = useRef<ReturnType<typeof setInterval> | undefined>(undefined);

  const [src, setSrc] = useState<string>();
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        // Proxy is the playback source; the original is the fallback while
        // prep is still running (or for pre-prep assets).
        const proxy = await signVariant(props.versionId, "proxy");
        const chosen = proxy ?? (await assetClient.signDownload({ versionId: props.versionId })).url;
        if (cancelled) return;
        setUsingProxy(proxy !== null);
        setSrc(chosen);
        const filmstrip = await signVariant(props.versionId, "filmstrip");
        if (filmstrip && !cancelled) setStrip({ url: filmstrip, cols: 0 });
        const wf = await signVariant(props.versionId, "waveform");
        if (wf && !cancelled) {
          const res = await fetch(wf);
          const j = (await res.json()) as { peaks: number[] };
          if (!cancelled) setWaveform(j.peaks);
        }
      } catch (e) {
        if (!cancelled) setError(String(e));
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [props.versionId]);

  // Transport keys — never while typing.
  useEffect(() => {
    const v = () => videoRef.current;
    const stopJog = () => {
      if (jogRef.current) clearInterval(jogRef.current);
      jogRef.current = undefined;
    };
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement;
      if (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.isContentEditable) return;
      const el = v();
      if (!el) return;
      switch (e.key.toLowerCase()) {
        case " ":
          e.preventDefault();
          stopJog();
          el.paused ? void el.play() : el.pause();
          break;
        case "l":
          stopJog();
          // L again while playing: speed up (1x → 2x → 4x, NLE-style).
          if (!el.paused && el.playbackRate < 4) el.playbackRate *= 2;
          else {
            el.playbackRate = 1;
            void el.play();
          }
          break;
        case "k":
          stopJog();
          el.pause();
          el.playbackRate = 1;
          break;
        case "j":
          // <video> can't play in reverse — jog backward at 4 steps/s.
          el.pause();
          if (!jogRef.current) {
            jogRef.current = setInterval(() => {
              const el2 = v();
              if (el2) el2.currentTime = Math.max(0, el2.currentTime - 0.25);
            }, 250);
          }
          break;
        case "arrowleft":
          e.preventDefault();
          stopJog();
          el.pause();
          el.currentTime = Math.max(0, el.currentTime - 1 / 24);
          break;
        case "arrowright":
          e.preventDefault();
          stopJog();
          el.pause();
          el.currentTime = Math.min(el.duration || 0, el.currentTime + 1 / 24);
          break;
      }
    };
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
      stopJog();
    };
  }, []);

  const scrub = (e: React.PointerEvent<HTMLDivElement>) => {
    const el = videoRef.current;
    if (!el || !el.duration) return;
    const rect = e.currentTarget.getBoundingClientRect();
    const frac = Math.min(1, Math.max(0, (e.clientX - rect.left) / rect.width));
    el.currentTime = frac * el.duration;
  };

  const fmt = (s: number) => {
    const m = Math.floor(s / 60);
    const sec = (s % 60).toFixed(1).padStart(4, "0");
    return `${m}:${sec}`;
  };

  return (
    <div className="overlay" onClick={props.onClose}>
      <div className="modal modal-wide player" role="dialog" aria-modal="true" onClick={(e) => e.stopPropagation()}>
        <div className="panel-header">
          <h3 className="truncate">{props.title ?? "Clip"}</h3>
          <span className="meta">
            {usingProxy === false && "original (proxy still rendering) · "}
            J/K/L · space · ←/→ frame
          </span>
          <button className="btn secondary" onClick={props.onClose}>
            Close
          </button>
        </div>
        {error && <div className="status error">{error}</div>}
        {src && (
          <video
            ref={videoRef}
            src={src}
            className="player-video"
            autoPlay
            controls={false}
            onTimeUpdate={(e) => setTime(e.currentTarget.currentTime)}
            onDurationChange={(e) => setDuration(e.currentTarget.duration)}
            onPlay={() => setPlaying(true)}
            onPause={() => setPlaying(false)}
            onClick={() => {
              const el = videoRef.current!;
              el.paused ? void el.play() : el.pause();
            }}
          />
        )}
        <div
          className="player-scrub"
          onPointerDown={(e) => {
            e.currentTarget.setPointerCapture(e.pointerId);
            scrub(e);
          }}
          onPointerMove={(e) => e.buttons === 1 && scrub(e)}
        >
          {strip && <img className="player-strip" src={strip.url} alt="" draggable={false} />}
          {waveform && <Waveform peaks={waveform} />}
          {duration > 0 && <div className="player-head" style={{ left: `${(time / duration) * 100}%` }} />}
        </div>
        <div className="toolbar" style={{ marginBottom: 0 }}>
          <button
            className="btn secondary"
            onClick={() => {
              const el = videoRef.current!;
              el.paused ? void el.play() : el.pause();
            }}
          >
            {playing ? "⏸" : "▶"}
          </button>
          <span className="meta">
            {fmt(time)} / {fmt(duration)}
          </span>
        </div>
      </div>
    </div>
  );
}

/** Peak envelope drawn once per data change; sits under the playhead. */
function Waveform(props: { peaks: number[] }) {
  const ref = useRef<HTMLCanvasElement>(null);
  useEffect(() => {
    const c = ref.current!;
    const ctx = c.getContext("2d")!;
    const w = (c.width = c.clientWidth || 600);
    const h = (c.height = 28);
    ctx.clearRect(0, 0, w, h);
    ctx.fillStyle = "rgba(139,124,246,0.7)";
    const n = props.peaks.length;
    for (let x = 0; x < w; x++) {
      const p = props.peaks[Math.floor((x / w) * n)] ?? 0;
      const bh = Math.max(1, p * h);
      ctx.fillRect(x, (h - bh) / 2, 1, bh);
    }
  }, [props.peaks]);
  return <canvas ref={ref} className="player-wave" />;
}
