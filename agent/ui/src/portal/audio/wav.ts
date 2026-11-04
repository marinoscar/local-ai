// Audio conversion for the Talk flow (survival-portal.md section 4.5 step 2): whatever the browser
// recorded becomes 16 kHz mono 16-bit PCM WAV, because whisper.cpp wants that and the node has no
// ffmpeg. The pure parts (length, resample, encode) are here and tested; the decode part needs a
// browser.

export const STT_RATE = 16000;
export const WAV_HEADER_BYTES = 44;

/** convertedLength is the number of samples at rate for a clip of seconds: ceil(duration x rate). */
export function convertedLength(seconds: number, rate = STT_RATE): number {
  return Math.ceil(seconds * rate);
}

/** resampleLinear resamples mono float samples by linear interpolation. */
export function resampleLinear(
  input: Float32Array,
  inRate: number,
  outRate: number,
): Float32Array {
  if (inRate === outRate) return input;
  const n = Math.ceil((input.length * outRate) / inRate);
  const out = new Float32Array(n);
  const ratio = inRate / outRate;
  for (let i = 0; i < n; i++) {
    const pos = i * ratio;
    const i0 = Math.floor(pos);
    const i1 = Math.min(i0 + 1, input.length - 1);
    const frac = pos - i0;
    const a = input[Math.min(i0, input.length - 1)] ?? 0;
    out[i] = a + (input[i1] - a) * frac;
  }
  return out;
}

/** encodeWav is a PCM16 mono WAV: the 44-byte RIFF header and the samples, clipped to [-1, 1]. */
export function encodeWav(samples: Float32Array, rate = STT_RATE): Uint8Array {
  const dataBytes = samples.length * 2;
  const buf = new ArrayBuffer(WAV_HEADER_BYTES + dataBytes);
  const v = new DataView(buf);
  const ascii = (off: number, s: string) => {
    for (let i = 0; i < s.length; i++) v.setUint8(off + i, s.charCodeAt(i));
  };
  ascii(0, "RIFF");
  v.setUint32(4, 36 + dataBytes, true);
  ascii(8, "WAVE");
  ascii(12, "fmt ");
  v.setUint32(16, 16, true); // fmt chunk size
  v.setUint16(20, 1, true); // PCM
  v.setUint16(22, 1, true); // mono
  v.setUint32(24, rate, true);
  v.setUint32(28, rate * 2, true); // byte rate
  v.setUint16(32, 2, true); // block align
  v.setUint16(34, 16, true); // bits per sample
  ascii(36, "data");
  v.setUint32(40, dataBytes, true);
  for (let i = 0; i < samples.length; i++) {
    const s = Math.max(-1, Math.min(1, samples[i]));
    v.setInt16(WAV_HEADER_BYTES + i * 2, s < 0 ? s * 0x8000 : s * 0x7fff, true);
  }
  return new Uint8Array(buf);
}

/** A recording, as the recorder hands it over. */
export type Recording =
  | { kind: "blob"; blob: Blob }
  | { kind: "pcm"; samples: Float32Array; sampleRate: number };

/** ConvertError is a recording the browser could not turn into samples (LAI-SVC-105). */
export class ConvertError extends Error {
  readonly code = "LAI-SVC-105";
}

/**
 * recordingToWav converts a recording to the 16 kHz mono WAV the STT route takes. A compressed blob
 * goes through decodeAudioData and an OfflineAudioContext of the target rate, which resamples and
 * mixes down to mono in one step.
 */
export async function recordingToWav(rec: Recording): Promise<Blob> {
  let samples: Float32Array;
  if (rec.kind === "pcm") {
    samples = resampleLinear(rec.samples, rec.sampleRate, STT_RATE);
  } else {
    try {
      const bytes = await rec.blob.arrayBuffer();
      const ctx = new AudioContext();
      let decoded: AudioBuffer;
      try {
        decoded = await ctx.decodeAudioData(bytes);
      } finally {
        void ctx.close();
      }
      const off = new OfflineAudioContext(
        1,
        Math.max(1, convertedLength(decoded.duration)),
        STT_RATE,
      );
      const src = off.createBufferSource();
      src.buffer = decoded;
      src.connect(off.destination);
      src.start();
      const rendered = await off.startRendering();
      samples = rendered.getChannelData(0);
    } catch {
      throw new ConvertError("audio could not be decoded");
    }
  }
  if (samples.length === 0) throw new ConvertError("empty recording");
  return new Blob([encodeWav(samples).buffer as ArrayBuffer], {
    type: "audio/wav",
  });
}
