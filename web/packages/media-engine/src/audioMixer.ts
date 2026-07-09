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
   * with no decodable audio are silent, not errors. */
  play(segments: Segment[], fromS: number): void {
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
          node.connect(this.master!);
          node.onended = () => {
            node.disconnect();
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
