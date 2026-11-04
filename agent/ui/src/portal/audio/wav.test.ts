import { describe, expect, it } from "vitest";
import {
  convertedLength,
  encodeWav,
  resampleLinear,
  STT_RATE,
  WAV_HEADER_BYTES,
} from "./wav";

const ascii = (b: Uint8Array, off: number, n: number) =>
  String.fromCharCode(...b.slice(off, off + n));

describe("encodeWav", () => {
  it("writes the 44-byte RIFF header of a 16 kHz mono PCM16 file", () => {
    const samples = new Float32Array(160); // 10 ms
    const wav = encodeWav(samples);
    const v = new DataView(wav.buffer);
    expect(wav.length).toBe(WAV_HEADER_BYTES + 320);
    expect(ascii(wav, 0, 4)).toBe("RIFF");
    expect(v.getUint32(4, true)).toBe(36 + 320);
    expect(ascii(wav, 8, 4)).toBe("WAVE");
    expect(ascii(wav, 12, 4)).toBe("fmt ");
    expect(v.getUint32(16, true)).toBe(16);
    expect(v.getUint16(20, true)).toBe(1); // PCM
    expect(v.getUint16(22, true)).toBe(1); // mono
    expect(v.getUint32(24, true)).toBe(16000);
    expect(v.getUint32(28, true)).toBe(32000); // byte rate
    expect(v.getUint16(32, true)).toBe(2); // block align
    expect(v.getUint16(34, true)).toBe(16); // bits
    expect(ascii(wav, 36, 4)).toBe("data");
    expect(v.getUint32(40, true)).toBe(320);
  });

  it("encodes and clips samples", () => {
    const wav = encodeWav(new Float32Array([0, 1, -1, 2, -2, 0.5]));
    const v = new DataView(wav.buffer);
    const at = (i: number) => v.getInt16(WAV_HEADER_BYTES + i * 2, true);
    expect(at(0)).toBe(0);
    expect(at(1)).toBe(32767);
    expect(at(2)).toBe(-32768);
    expect(at(3)).toBe(32767);
    expect(at(4)).toBe(-32768);
    expect(at(5)).toBe(Math.trunc(0.5 * 0x7fff));
  });

  it("is a valid header for an empty clip", () => {
    const wav = encodeWav(new Float32Array(0));
    expect(wav.length).toBe(WAV_HEADER_BYTES);
    expect(new DataView(wav.buffer).getUint32(40, true)).toBe(0);
  });
});

describe("resampling", () => {
  it("convertedLength is ceil(duration x 16000)", () => {
    expect(convertedLength(1)).toBe(16000);
    expect(convertedLength(0.5)).toBe(8000);
    expect(convertedLength(1.00001)).toBe(16001);
  });

  it("48 kHz to 16 kHz gives a third of the samples", () => {
    const input = new Float32Array(48000); // 1 s
    expect(resampleLinear(input, 48000, STT_RATE).length).toBe(16000);
  });

  it("44.1 kHz to 16 kHz length is ceil(duration x 16000)", () => {
    const input = new Float32Array(44100 * 2);
    expect(resampleLinear(input, 44100, STT_RATE).length).toBe(32000);
  });

  it("keeps a ramp a ramp", () => {
    const input = Float32Array.from({ length: 300 }, (_, i) => i / 300);
    const out = resampleLinear(input, 48000, STT_RATE);
    expect(out.length).toBe(100);
    for (let i = 1; i < out.length; i++) expect(out[i]).toBeGreaterThan(out[i - 1]);
    expect(out[10]).toBeCloseTo(30 / 300, 5);
  });

  it("returns the input when the rates match", () => {
    const input = new Float32Array([0.1, 0.2]);
    expect(resampleLinear(input, 16000, 16000)).toBe(input);
  });
});
