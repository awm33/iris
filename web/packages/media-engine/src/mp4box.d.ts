// Minimal ambient types for the mp4box surface this package touches —
// the npm package ships no types.
declare module "mp4box" {
  export interface MP4Info {
    duration: number;
    timescale: number;
    videoTracks: MP4Track[];
  }
  export interface MP4Track {
    id: number;
    codec: string;
    nb_samples: number;
    video: { width: number; height: number };
  }
  export interface Sample {
    data: Uint8Array;
    cts: number;
    duration: number;
    timescale: number;
    is_sync: boolean;
  }
  export interface ISOFile {
    onReady: (info: MP4Info) => void;
    onError: (e: string) => void;
    onSamples: (id: number, user: unknown, samples: Sample[]) => void;
    appendBuffer(buf: ArrayBuffer & { fileStart: number }): number;
    flush(): void;
    setExtractionOptions(trackId: number, user: unknown, opts: { nbSamples: number }): void;
    start(): void;
    getTrackById(id: number): {
      mdia: { minf: { stbl: { stsd: { entries: Array<Record<string, { write(s: DataStream): void } | undefined>> } } } };
    };
  }
  export function createFile(): ISOFile;
  export class DataStream {
    constructor(arrayBuffer: ArrayBuffer | undefined, byteOffset: number, endianness: boolean);
    static BIG_ENDIAN: boolean;
    buffer: ArrayBuffer;
  }
}
