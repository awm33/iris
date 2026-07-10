// Browser-only WebAudio shell (compositor arc, PR 24): decode proxy audio
// with decodeAudioData, schedule statically on the AudioContext clock.
// The wall clock still drives the playhead; audio is anchored to the
// context clock at play start — drift between the two over seconds-scale
// dogfood timelines is inaudible (recorded deviation: an audio-clock-
// driven playhead lands when timelines grow long enough to care).
import { audioSchedule } from "./audioSchedule";
import type { Segment } from "./schedule";

// Decoded AudioBuffers are float PCM — ~230MB per 10 minutes of stereo
// 48kHz, so this cap is a real memory budget (~1.4GB worst case), same
// caveat class as the video decoder cache. Fine at dogfood clip lengths.
const MAX_BUFFERS = 6;

export class AudioMixer {
  private ctx: AudioContext | null = null;
  private master: GainNode | null = null;
  private analyser: AnalyserNode | null = null;
  // null = a cached MISS: the source has no decodable audio (normal for
  // audio-less video like mock takes) — refetching the whole proxy per
  // play to rediscover that would be churn proportional to timeline size.
  private buffers = new Map<string, Promise<AudioBuffer | null>>();
  private active: AudioBufferSourceNode[] = [];
  private duckGains: GainNode[] = [];
  private playToken = 0;

  constructor(
    private readonly srcFor: (sourceId: string) => Promise<string>,
    /** Called when the context can't run (autoplay policy without sticky
     * activation, e.g. Safari) — silence should be visible, not silent. */
    private readonly onBlocked?: () => void,
  ) {}

  static supported(): boolean {
    return typeof AudioContext !== "undefined";
  }

  private ensureCtx(): AudioContext {
    if (!this.ctx) {
      this.ctx = new AudioContext();
      this.master = this.ctx.createGain();
      this.analyser = this.ctx.createAnalyser();
      this.analyser.fftSize = 256;
      this.master.connect(this.analyser);
      this.analyser.connect(this.ctx.destination);
    }
    return this.ctx;
  }

  private bufferFor(sourceId: string): Promise<AudioBuffer | null> {
    const hit = this.buffers.get(sourceId);
    if (hit) {
      this.buffers.delete(sourceId);
      this.buffers.set(sourceId, hit); // LRU touch
      return hit;
    }
    const ctx = this.ensureCtx();
    const decoded = this.srcFor(sourceId)
      .then((url) => fetch(url))
      .then((res) => {
        if (!res.ok) throw new Error(`fetch ${res.status}`);
        return res.arrayBuffer();
      })
      // decode failure = no audio track — a durable fact about the source,
      // cached as null. Only fetch/HTTP failures (transient signing) fall
      // through to the rejection-delete below and retry next play.
      .then((buf) => ctx.decodeAudioData(buf).catch(() => null));
    decoded.catch(() => {
      if (this.buffers.get(sourceId) === decoded) this.buffers.delete(sourceId);
    });
    this.buffers.set(sourceId, decoded);
    for (const key of [...this.buffers.keys()]) {
      if (this.buffers.size <= MAX_BUFFERS) break;
      this.buffers.delete(key);
    }
    return decoded;
  }

  /** Schedule every audible segment from fromS. Buffers that resolve after
   * their start time begin immediately with the offset advanced by the
   * lateness; a stale play (token moved on) schedules nothing. Sources
   * with no decodable audio are silent, not errors.
   *
   * ducks: merged speech windows (doc-runtime's duckWindows) — every
   * NON-speech segment's gain follows the deterministic curve the export
   * renders (g = 1 − 0.75·coverage, 0.15s linear ramps), scheduled as
   * WebAudio automation at play time. */
  play(segments: Segment[], fromS: number, ducks: { start: number; end: number }[] = []): void {
    this.stop();
    const token = ++this.playToken;
    const ctx = this.ensureCtx();
    // Play is user-initiated (space/▶) so sticky activation normally covers
    // resume() from this async context — but not on every browser: surface
    // a still-suspended context instead of being silently silent.
    void ctx.resume().then(() => {
      if (ctx.state !== "running") this.onBlocked?.();
    });
    // Anchor at NOW (minus reported output latency): a comfort pad here is
    // a constant A/V skew for the whole play — the late-start branch below
    // already absorbs scheduling slop by advancing the offset.
    const anchor = ctx.currentTime - (ctx.outputLatency || ctx.baseLatency || 0);
    for (const e of audioSchedule(segments, fromS)) {
      void this.bufferFor(e.sourceId)
        .then((buf) => {
          if (token !== this.playToken || buf === null) return;
          const node = ctx.createBufferSource();
          node.buffer = buf;
          let dest: AudioNode = this.master!;
          if (!e.speech && ducks.length > 0) {
            const g = ctx.createGain();
            g.connect(this.master!);
            this.automateDuck(g.gain, ducks, fromS, anchor, ctx);
            this.duckGains.push(g);
            dest = g;
          }
          node.connect(dest);
          node.onended = () => {
            node.disconnect();
            // Deterministic teardown: an idle GainNode still costs a pull
            // per render quantum until GC gets around to it.
            if (dest !== this.master) {
              dest.disconnect();
              this.duckGains = this.duckGains.filter((n) => n !== dest);
            }
            this.active = this.active.filter((n) => n !== node);
          };
          const startAt = anchor + e.whenS;
          const late = ctx.currentTime - startAt;
          if (late <= 0) {
            node.start(startAt, e.offsetS, e.durationS);
          } else if (late < e.durationS) {
            node.start(ctx.currentTime, e.offsetS + late, e.durationS - late);
          } else {
            return; // resolved after its whole window — nothing to play
          }
          this.active.push(node);
        })
        .catch(() => {
          /* no/undecodable audio track → silent */
        });
    }
  }

  /** Duck-curve automation: piecewise-linear, knees at each window's four
   * ramp points plus the crossings of overlapping adjacent ramps — the
   * exact max()-of-trapezoids curve the export's volume expression
   * evaluates per frame. Points in the past collapse into the initial
   * setValueAtTime; ramps schedule only forward. */
  private automateDuck(
    gain: AudioParam,
    ducks: { start: number; end: number }[],
    fromS: number,
    anchor: number,
    ctx: AudioContext,
  ): void {
    const R = 0.15;
    const L = 0.25;
    const cov = (t: number) => {
      let c = 0;
      for (const w of ducks) {
        c = Math.max(c, Math.max(0, Math.min(1, Math.min((t - w.start) / R, (w.end + R - t) / R))));
      }
      return c;
    };
    const g = (t: number) => 1 - (1 - L) * cov(t);
    const knees: number[] = [];
    for (let i = 0; i < ducks.length; i++) {
      const w = ducks[i];
      knees.push(w.start, w.start + R, w.end, w.end + R);
      if (w.end - w.start < R) {
        // Sub-ramp window: the trapezoid's apex falls BETWEEN the edge
        // knees — without it the preview flat-lines below the export's
        // per-frame peak.
        knees.push((w.start + w.end + R) / 2);
      }
      const next = ducks[i + 1];
      if (next && next.start - w.end < 2 * R) {
        knees.push((w.end + R + next.start) / 2); // release×attack crossing
      }
    }
    // Late resolve (first-play decode can take seconds) and output latency
    // both mean "now" is PAST fromS: anchor the initial value at the
    // CURRENT timeline position and only ramp to knees still ahead —
    // otherwise past-dated ramps complete instantly and the play-start
    // value pins the gain wrong for the rest of the play (the content
    // path's offset-advance branch, mirrored for automation).
    const nowT = fromS + Math.max(0, ctx.currentTime - anchor);
    const future = [...new Set(knees)].filter((t) => t > nowT).sort((a, b) => a - b);
    gain.setValueAtTime(g(nowT), ctx.currentTime);
    for (const t of future) {
      gain.linearRampToValueAtTime(g(t), anchor + (t - fromS));
    }
  }

  stop(): void {
    this.playToken++;
    for (const node of this.active) {
      try {
        node.stop();
      } catch {
        /* never started or already stopped */
      }
      node.disconnect();
    }
    this.active = [];
    for (const g of this.duckGains) g.disconnect();
    this.duckGains = [];
  }

  /** RMS level 0..1 from the master bus — meters, and the proof in tests
   * that something is actually audible. */
  level(): number {
    if (!this.analyser) return 0;
    const data = new Float32Array(this.analyser.fftSize);
    this.analyser.getFloatTimeDomainData(data);
    let sum = 0;
    for (const v of data) sum += v * v;
    return Math.sqrt(sum / data.length);
  }

  dispose(): void {
    this.stop();
    void this.ctx?.close();
    this.ctx = null;
    this.master = null;
    this.analyser = null;
    this.buffers.clear();
  }
}
