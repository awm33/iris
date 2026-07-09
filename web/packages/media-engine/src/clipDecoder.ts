/// <reference path="./mp4box.d.ts" />
// Browser-only WebCodecs shell: mp4box demux → VideoDecoder → VideoFrames.
// PR 22 scope: whole-file fetch (proxies are small by construction — the
// prep pipeline caps them at 720p), first video track only, decode-forward
// from the nearest sync sample. Streaming appendBuffer and audio land with
// the compositor/audio slices.
import * as MP4Box from "mp4box";
import { buildIndex } from "./buildIndex";
import { type SampleMeta, seekPointIndex, streamEnd } from "./sampleIndex";

export interface ClipInfo {
  durationS: number;
  width: number;
  height: number;
  codec: string;
}

interface DemuxedSample {
  data: Uint8Array;
  /** Presentation timestamp, µs. */
  timestampUs: number;
  durationUs: number;
  sync: boolean;
}

export class ClipDecoder {
  private constructor(
    readonly info: ClipInfo,
    private readonly config: VideoDecoderConfig,
    private readonly samples: DemuxedSample[],
    private readonly index: SampleMeta[],
  ) {}

  static supported(): boolean {
    return typeof VideoDecoder !== "undefined";
  }

  get endS(): number {
    return streamEnd(this.index);
  }

  static async open(url: string, signal?: AbortSignal): Promise<ClipDecoder> {
    const res = await fetch(url, { signal });
    if (!res.ok) throw new Error(`fetch ${res.status}`);
    const buf = await res.arrayBuffer();

    const file = MP4Box.createFile();
    // The whole buffer is appended in one call, so mp4box parses and
    // extracts SYNCHRONOUSLY — presence checks after the calls replace
    // callbacks (onError is dead wiring in mp4box 0.5.x: never invoked;
    // an unparseable container simply never fires onReady).
    let info: MP4Box.MP4Info | null = null;
    file.onReady = (i) => {
      info = i;
    };
    (buf as ArrayBuffer & { fileStart: number }).fileStart = 0;
    file.appendBuffer(buf as ArrayBuffer & { fileStart: number });
    file.flush();
    // TS can't see the synchronous callback assignment — snapshot it.
    const parsed = info as MP4Box.MP4Info | null;
    if (!parsed) throw new Error("unparseable container — the engine plays MP4 only");

    const track = parsed.videoTracks[0];
    if (!track) throw new Error("no video track");

    const samples: DemuxedSample[] = [];
    file.onSamples = (_id: number, _user: unknown, batch: MP4Box.Sample[]) => {
      for (const s of batch) {
        samples.push({
          data: s.data,
          timestampUs: Math.round((s.cts / s.timescale) * 1e6),
          durationUs: Math.round((s.duration / s.timescale) * 1e6),
          sync: s.is_sync,
        });
      }
    };
    file.setExtractionOptions(track.id, null, { nbSamples: track.nb_samples });
    file.start();
    if (samples.length < track.nb_samples) {
      throw new Error(`demux incomplete: ${samples.length}/${track.nb_samples} samples`);
    }
    // Samples stay in mp4box delivery order = DECODE order. Sorting by
    // presentation time here once broke every real proxy: prep's x264
    // output has B-frames (decode order ≠ presentation order), and
    // VideoDecoder requires decode order — timestamps carry cts.
    // buildIndex removes the edit-list bias (min cts → 0) so the
    // compositor's clock and seeks aren't skewed by ~2 frame durations.
    const built = buildIndex(samples);
    for (let i = 0; i < samples.length; i++) samples[i].timestampUs = built.timestampsUs[i];
    const index: SampleMeta[] = built.index;

    const config: VideoDecoderConfig = {
      codec: track.codec,
      codedWidth: track.video.width,
      codedHeight: track.video.height,
      description: extractDescription(file, track.id),
    };
    // Fail at open (clear message), not on first decode (opaque error).
    const support = await VideoDecoder.isConfigSupported(config);
    if (!support.supported) throw new Error(`codec ${track.codec} not decodable here`);

    return new ClipDecoder(
      {
        durationS: parsed.duration / parsed.timescale,
        width: track.video.width,
        height: track.video.height,
        codec: track.codec,
      },
      config,
      samples,
      index,
    );
  }

  /** Decoded frames in presentation order from the nearest sync sample at
   * or before fromS. The consumer OWNS each yielded frame (close it).
   * Frames before fromS are pre-roll from the sync point and are closed
   * internally, never yielded. */
  async *frames(fromS: number, signal?: AbortSignal): AsyncGenerator<VideoFrame> {
    const startIdx = seekPointIndex(this.index, fromS);
    const fromUs = Math.round(fromS * 1e6) - 1_000; // µs slack: rounding must not drop the target frame
    const out: VideoFrame[] = [];
    let waiter: (() => void) | null = null;
    let decodeError: Error | null = null;

    const decoder = new VideoDecoder({
      output: (frame) => {
        if (frame.timestamp < fromUs) {
          frame.close(); // pre-roll
          return;
        }
        out.push(frame);
        waiter?.();
      },
      error: (e) => {
        decodeError = e instanceof Error ? e : new Error(String(e));
        waiter?.();
      },
    });
    decoder.configure(this.config);

    try {
      let fed = startIdx;
      let flushed = false;
      let flushDone = false;
      // Wake the park when the decoder wants more input (dequeue), emits a
      // frame (output cb), errors, or finishes flushing.
      decoder.ondequeue = () => waiter?.();
      for (;;) {
        if (signal?.aborted) return;
        if (decodeError) throw decodeError;
        // Feed while the decoder is hungry AND our undelivered-output pile
        // is small. Unclosed VideoFrames pin hardware decoder buffers —
        // letting `out` grow while the consumer is slow wedges the decoder
        // (it stops dequeuing input entirely until buffers return).
        while (fed < this.samples.length && decoder.decodeQueueSize < 8 && out.length < 4) {
          const s = this.samples[fed++];
          decoder.decode(
            new EncodedVideoChunk({
              type: s.sync ? "key" : "delta",
              timestamp: s.timestampUs,
              duration: s.durationUs,
              data: s.data as BufferSource,
            }),
          );
        }
        if (fed >= this.samples.length && !flushed) {
          flushed = true;
          // The flush PROMISE is the end-of-stream signal: it resolves only
          // after every reorder-held output has been emitted. Polling
          // decodeQueueSize instead once truncated the B-frame tail —
          // the queue drains when inputs are ACCEPTED, not presented.
          decoder.flush().then(
            () => {
              flushDone = true;
              waiter?.();
            },
            () => {
              flushDone = true; // error (if real) surfaces via the error cb
              waiter?.();
            },
          );
        }
        while (out.length > 0) yield out.shift()!;
        if (flushDone && out.length === 0) return;
        // LEVEL check before parking: wake events that fired while this
        // generator was suspended at a yield are lost (waiter unset), so
        // an edge-triggered park deadlocks — e.g. every dequeue spent
        // mid-yield leaves a hungry, silent decoder. If feeding is
        // possible, loop instead of parking. Parking is then safe: either
        // work is in flight (decodeQueueSize > 0 → dequeue/output will
        // fire) or everything is fed (flush completion will fire).
        if (fed < this.samples.length && decoder.decodeQueueSize < 8 && out.length < 4) continue;
        await new Promise<void>((resolve) => {
          waiter = resolve;
        });
        waiter = null;
      }
    } finally {
      for (const f of out) f.close();
      if (decoder.state !== "closed") decoder.close();
    }
  }
}

/** avcC/hvcC/etc. decoder-specific bytes from the sample description box —
 * VideoDecoderConfig.description. Standard mp4box recipe: serialize the
 * config box minus its 8-byte header. */
function extractDescription(file: MP4Box.ISOFile, trackId: number): Uint8Array {
  const trak = file.getTrackById(trackId);
  for (const entry of trak.mdia.minf.stbl.stsd.entries) {
    const box = entry.avcC ?? entry.hvcC ?? entry.vpcC ?? entry.av1C;
    if (box) {
      const stream = new MP4Box.DataStream(undefined, 0, MP4Box.DataStream.BIG_ENDIAN);
      box.write(stream);
      return new Uint8Array(stream.buffer, 8);
    }
  }
  throw new Error("no decoder config box in sample description");
}
