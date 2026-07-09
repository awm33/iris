// Browser-only WebAudio shell (compositor arc, PR 24): decode proxy audio
// with decodeAudioData, schedule statically on the AudioContext clock.
// The wall clock still drives the playhead; audio is anchored to the
// context clock at play start — drift between the two over seconds-scale
// dogfood timelines is inaudible (recorded deviation: an audio-clock-
// driven playhead lands when timelines grow long enough to care).
import { audioSchedule } from "./audioSchedule";
import type { Segment } from "./schedule";

const MAX_BUFFERS = 6; // decoded AudioBuffers are float PCM — LRU beyond this

export class AudioMixer {
  private ctx: AudioContext | null = null;
  private master: GainNode | null = null;
  private analyser: AnalyserNode | null = null;
  private buffers = new Map<string, Promise<AudioBuffer>>();
  private active: AudioBufferSourceNode[] = [];
  private playToken = 0;

  constructor(private readonly srcFor: (sourceId: string) => Promise<string>) {}

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

  private bufferFor(sourceId: string): Promise<AudioBuffer> {
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
      .then((buf) => ctx.decodeAudioData(buf));
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
    void ctx.resume(); // play is always user-initiated (space/▶) — activation is fresh
    const anchor = ctx.currentTime + 0.08;
    for (const e of audioSchedule(segments, fromS)) {
      void this.bufferFor(e.sourceId)
        .then((buf) => {
          if (token !== this.playToken) return;
          const node = ctx.createBufferSource();
          node.buffer = buf;
          node.connect(this.master!);
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
