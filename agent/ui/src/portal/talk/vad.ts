// Voice activity for the auto-stop of the Talk recorder (survival-portal.md section 4.5 step 1):
// stop after 1.5 s below -45 dBFS once speech was heard, or after 60 s.

export const SILENCE_DB = -45;
export const SILENCE_MS = 1500;
export const MAX_RECORD_MS = 60000;

/** levelDb is the RMS level of float samples in dBFS (-Infinity for pure silence). */
export function levelDb(samples: Float32Array): number {
  if (samples.length === 0) return -Infinity;
  let sum = 0;
  for (let i = 0; i < samples.length; i++) sum += samples[i] * samples[i];
  const rms = Math.sqrt(sum / samples.length);
  return rms === 0 ? -Infinity : 20 * Math.log10(rms);
}

/** meterFraction maps a level to 0..1 for the level meter (-60 dBFS is empty, 0 dBFS is full). */
export function meterFraction(db: number): number {
  if (!Number.isFinite(db)) return 0;
  return Math.min(1, Math.max(0, (db + 60) / 60));
}

/** formatClock is the 0:04 of the timer. */
export function formatClock(ms: number): string {
  const s = Math.floor(ms / 1000);
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

/**
 * EndpointDetector says when to stop: feed it (level, time) pairs in order. It answers "silence"
 * after SILENCE_MS below SILENCE_DB following speech, "limit" at MAX_RECORD_MS, otherwise null.
 */
export class EndpointDetector {
  private heard = false;
  private quietSince: number | null = null;
  private readonly start: number;

  constructor(startMs: number) {
    this.start = startMs;
  }

  get heardSpeech(): boolean {
    return this.heard;
  }

  push(db: number, nowMs: number): "silence" | "limit" | null {
    if (nowMs - this.start >= MAX_RECORD_MS) return "limit";
    if (db > SILENCE_DB) {
      this.heard = true;
      this.quietSince = null;
      return null;
    }
    if (!this.heard) return null;
    if (this.quietSince === null) this.quietSince = nowMs;
    return nowMs - this.quietSince >= SILENCE_MS ? "silence" : null;
  }
}
