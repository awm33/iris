// Presentation queue between the decoder and the painter. Generic over
// anything with a µs timestamp and close() so tests can stub VideoFrames
// (which don't exist in jsdom). Frames MUST be closed exactly once —
// VideoFrames pin GPU/decoder memory until closed.

export interface TimedFrame {
  /** Presentation timestamp, microseconds (VideoFrame.timestamp). */
  timestamp: number;
  close(): void;
}

export class FrameQueue<F extends TimedFrame> {
  private frames: F[] = [];

  constructor(readonly capacity: number) {}

  get size(): number {
    return this.frames.length;
  }

  /** Room for more decode output? The decoder loop checks this before
   * feeding more chunks (backpressure). */
  get full(): boolean {
    return this.frames.length >= this.capacity;
  }

  /** Append in PRESENTATION order (decoder output order). Over capacity the OLDEST frame is closed and
   * dropped — the painter has fallen behind and old frames are the ones
   * that will never be shown. */
  push(f: F): void {
    this.frames.push(f);
    while (this.frames.length > this.capacity) {
      this.frames.shift()!.close();
    }
  }

  /** The frame to paint at tUs: the LATEST frame with timestamp <= tUs.
   * Frames older than it are closed (their display window has passed);
   * newer frames stay queued. Returns null when nothing is due yet — the
   * caller keeps showing the previous paint. Ownership of the returned
   * frame transfers to the caller (paint, then close). */
  takeUpTo(tUs: number): F | null {
    let due = -1;
    for (let i = 0; i < this.frames.length; i++) {
      if (this.frames[i].timestamp <= tUs) due = i;
      else break;
    }
    if (due === -1) return null;
    for (let i = 0; i < due; i++) this.frames[i].close();
    const frame = this.frames[due];
    this.frames = this.frames.slice(due + 1);
    return frame;
  }

  /** Close everything (seek, teardown). */
  clear(): void {
    for (const f of this.frames) f.close();
    this.frames = [];
  }
}
