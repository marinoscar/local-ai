import type { Recording } from "../audio/wav";
import workletUrl from "./pcm-worklet.js?url";
import { EndpointDetector, levelDb } from "./vad";

// The Talk recorder (survival-portal.md section 4.5 step 1): getUserMedia, MediaRecorder (Opus in
// WebM or Ogg), a level meter, and the auto-stop. Without MediaRecorder an AudioWorklet captures raw
// samples instead. http://127.0.0.1 is a secure context, so no HTTPS is needed.

/** RecorderError carries the doctor code of the screen to show: 103 mic blocked or missing, 104 can't record. */
export class RecorderError extends Error {
  readonly code: "LAI-SVC-103" | "LAI-SVC-104";
  constructor(code: "LAI-SVC-103" | "LAI-SVC-104", message: string) {
    super(message);
    this.code = code;
  }
}

const MIME_CANDIDATES = [
  "audio/webm;codecs=opus",
  "audio/ogg;codecs=opus",
  "audio/webm",
  "audio/ogg",
];

/** pickMime is the first recording type the browser supports, or "" to let it choose. */
export function pickMime(
  isSupported: (t: string) => boolean = (t) => MediaRecorder.isTypeSupported(t),
): string {
  return MIME_CANDIDATES.find((t) => isSupported(t)) ?? "";
}

export interface RecorderCallbacks {
  /** Level 0..1 and elapsed milliseconds, about 20 times a second. */
  onTick: (db: number, elapsedMs: number) => void;
  /** The endpoint detector wants to stop (silence after speech, or 60 s). */
  onAutoStop: () => void;
}

export class Recorder {
  private stream: MediaStream | null = null;
  private ctx: AudioContext | null = null;
  private rec: MediaRecorder | null = null;
  private chunks: Blob[] = [];
  private pcm: Float32Array[] = [];
  private node: AudioWorkletNode | null = null;
  private timer = 0;
  private autoStopped = false;
  private cancelled = false;

  constructor(private readonly cb: RecorderCallbacks) {}

  /** start asks for the microphone and begins recording. It throws RecorderError. */
  async start(): Promise<void> {
    if (!navigator.mediaDevices?.getUserMedia) {
      throw new RecorderError("LAI-SVC-104", "no getUserMedia");
    }
    const hasRecorder = typeof MediaRecorder !== "undefined";
    const hasWorklet =
      typeof AudioContext !== "undefined" &&
      typeof AudioWorkletNode !== "undefined";
    if (!hasRecorder && !hasWorklet) {
      throw new RecorderError("LAI-SVC-104", "cannot record");
    }
    this.cancelled = false;
    let stream: MediaStream;
    try {
      stream = await navigator.mediaDevices.getUserMedia({
        audio: {
          channelCount: 1,
          echoCancellation: true,
          noiseSuppression: true,
        },
      });
    } catch {
      throw new RecorderError("LAI-SVC-103", "microphone unavailable");
    }
    if (this.cancelled) {
      // Esc came while the browser asked for the microphone: let it go again.
      stream.getTracks().forEach((t) => t.stop());
      return;
    }
    this.stream = stream;
    this.chunks = [];
    this.pcm = [];
    this.autoStopped = false;

    // The meter (and the raw capture, when there is no MediaRecorder) share one AudioContext.
    const ctx = typeof AudioContext !== "undefined" ? new AudioContext() : null;
    this.ctx = ctx;
    let analyser: AnalyserNode | null = null;
    if (ctx) {
      const src = ctx.createMediaStreamSource(stream);
      analyser = ctx.createAnalyser();
      analyser.fftSize = 1024;
      src.connect(analyser);
      if (!hasRecorder) {
        await ctx.audioWorklet.addModule(workletUrl);
        this.node = new AudioWorkletNode(ctx, "pcm-capture");
        this.node.port.onmessage = (e: MessageEvent<Float32Array>) => {
          this.pcm.push(e.data);
        };
        src.connect(this.node);
        // Some browsers only run a worklet that is pulled by the destination: a muted path does it.
        const mute = ctx.createGain();
        mute.gain.value = 0;
        this.node.connect(mute);
        mute.connect(ctx.destination);
      }
    }
    if (hasRecorder) {
      const mime = pickMime();
      this.rec = new MediaRecorder(
        stream,
        mime ? { mimeType: mime } : undefined,
      );
      this.rec.ondataavailable = (e) => {
        if (e.data.size > 0) this.chunks.push(e.data);
      };
      this.rec.start(250);
    }

    const t0 = performance.now();
    const endpoint = new EndpointDetector(t0);
    const buf = new Float32Array(analyser?.fftSize ?? 1);
    this.timer = window.setInterval(() => {
      const now = performance.now();
      let db = -Infinity;
      if (analyser) {
        analyser.getFloatTimeDomainData(buf);
        db = levelDb(buf);
      }
      this.cb.onTick(db, now - t0);
      if (!this.autoStopped && endpoint.push(db, now) !== null) {
        this.autoStopped = true;
        this.cb.onAutoStop();
      }
    }, 50);
  }

  /** stop ends the recording and gives it back. */
  async stop(): Promise<Recording> {
    window.clearInterval(this.timer);
    let out: Recording;
    if (this.rec) {
      const rec = this.rec;
      const done = new Promise<void>((resolve) => {
        rec.onstop = () => resolve();
      });
      if (rec.state !== "inactive") rec.stop();
      await done;
      out = {
        kind: "blob",
        blob: new Blob(this.chunks, { type: rec.mimeType || "audio/webm" }),
      };
    } else {
      const total = this.pcm.reduce((n, c) => n + c.length, 0);
      const samples = new Float32Array(total);
      let off = 0;
      for (const c of this.pcm) {
        samples.set(c, off);
        off += c.length;
      }
      out = {
        kind: "pcm",
        samples,
        sampleRate: this.ctx?.sampleRate ?? 48000,
      };
    }
    this.release();
    return out;
  }

  /** cancel throws the recording away (Esc). */
  cancel(): void {
    this.cancelled = true;
    window.clearInterval(this.timer);
    if (this.rec && this.rec.state !== "inactive") {
      this.rec.onstop = null;
      this.rec.stop();
    }
    this.release();
  }

  private release(): void {
    this.node?.disconnect();
    this.node = null;
    this.stream?.getTracks().forEach((t) => t.stop());
    this.stream = null;
    if (this.ctx) void this.ctx.close();
    this.ctx = null;
    this.rec = null;
  }
}
