/// <reference path="./mp4box.d.ts" />
// Browser-only WebCodecs shell: mp4box demux → VideoDecoder → VideoFrames.
// PR 22 scope: whole-file fetch (proxies are small by construction — the
// prep pipeline caps them at 720p), first video track only, decode-forward
// from the nearest sync sample. Streaming appendBuffer and audio land with
// the compositor/audio slices.
import * as MP4Box from "mp4box";
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
    const ready = new Promise<MP4Box.MP4Info>((resolve, reject) => {
      file.onReady = resolve;
      file.onError = (e: string) => reject(new Error(e));
    });
    (buf as ArrayBuffer & { fileStart: number }).fileStart = 0;
    file.appendBuffer(buf as ArrayBuffer & { fileStart: number });
    file.flush();
    const info = await ready;

    const track = info.videoTracks[0];
    if (!track) throw new Error("no video track");

    const samples: DemuxedSample[] = [];
    const done = new Promise<void>((resolve) => {
      file.onSamples = (_id: number, _user: unknown, batch: MP4Box.Sample[]) => {
        for (const s of batch) {
          samples.push({
            data: s.data,
            timestampUs: Math.round((s.cts / s.timescale) * 1e6),
            durationUs: Math.round((s.duration / s.timescale) * 1e6),
            sync: s.is_sync,
          });
        }
        if (samples.length >= track.nb_samples) resolve();
      };
    });
    file.setExtractionOptions(track.id, null, { nbSamples: track.nb_samples });
    file.start();
    await done;
    samples.sort((a, b) => a.timestampUs - b.timestampUs);

    const index: SampleMeta[] = samples.map((s) => ({
      t: s.timestampUs / 1e6,
      duration: s.durationUs / 1e6,
      sync: s.sync,
    }));

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
        durationS: info.duration / info.timescale,
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
      for (;;) {
        if (signal?.aborted) return;
        if (decodeError) throw decodeError;
        // Feed while the decoder is hungry; flush once everything is in.
        while (fed < this.samples.length && decoder.decodeQueueSize < 8) {
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
          void decoder.flush().catch(() => {}); // error surfaces via callback
        }
        while (out.length > 0) yield out.shift()!;
        if (flushed && decoder.decodeQueueSize === 0 && out.length === 0) return;
        // Park until the decoder produces something (or errors).
        await new Promise<void>((resolve) => {
          waiter = resolve;
          // Also wake on a timer: decodeQueueSize draining without output
          // (tail of stream) must not hang the generator.
          setTimeout(resolve, 15);
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
