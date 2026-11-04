// The speak pipeline of the Talk and Photo flows (survival-portal.md section 4.5 step 5): each
// sentence goes to the TTS route as soon as it is complete, at most AHEAD requests in flight beyond
// the one that is playing, and the WAVs are played strictly in order through one player. Everything
// that touches the network or the speaker is behind TtsDeps, so the queue is unit tested with fakes.

export const AHEAD = 2;

export interface TtsDeps {
  /**
   * fetchWav turns one sentence into audio (one or more WAV blobs, when the agent made it split a
   * long text). null means no voice is installed (LAI-SVC-022): the queue then goes text-only.
   */
  fetchWav(text: string, signal: AbortSignal): Promise<Blob[] | null>;
  /** play plays one WAV and resolves when it ended, or when signal aborted. */
  play(wav: Blob, signal: AbortSignal): Promise<void>;
}

export interface TtsEvents {
  /** The first sound is about to play. */
  onSpeak?: () => void;
  /** No voice is installed: the rest of the answer is text only. */
  onTextOnly?: () => void;
  /** A request failed (once); the sentence is skipped. */
  onError?: (e: unknown) => void;
}

interface Item {
  text: string;
  wavs?: Promise<Blob[] | null>;
}

export class TtsQueue {
  private items: Item[] = [];
  private at = 0; // index of the sentence being awaited or played
  private ended = false;
  private stopped = false;
  private textOnly = false;
  private errored = false;
  private spoke = false;
  private running = false;
  private ctl = new AbortController();
  private readonly cache: Blob[] = [];
  private resolveDone!: () => void;
  /** Resolves when every sentence was played (or skipped), or after stop(). */
  readonly finished: Promise<void>;

  constructor(
    private readonly deps: TtsDeps,
    private readonly ev: TtsEvents = {},
  ) {
    this.finished = new Promise<void>((r) => {
      this.resolveDone = r;
    });
  }

  /** enqueue adds a sentence. */
  enqueue(text: string): void {
    if (this.stopped || this.ended || !text.trim()) return;
    this.items.push({ text });
    this.pump();
    void this.run();
  }

  /** end says no more sentences will come. */
  end(): void {
    this.ended = true;
    void this.run();
  }

  /** stop aborts every request and the sound, and drops what is queued. The cache is kept. */
  stop(): void {
    if (this.stopped) return;
    this.stopped = true;
    this.ctl.abort();
    this.resolveDone();
  }

  /** wavs is the audio played so far, for Repeat. */
  get cached(): readonly Blob[] {
    return this.cache;
  }

  get isTextOnly(): boolean {
    return this.textOnly;
  }

  private startFetch(it: Item): void {
    if (it.wavs || this.textOnly) return;
    it.wavs = this.deps.fetchWav(it.text, this.ctl.signal).catch((e: unknown) => {
      if (!this.stopped && !this.errored) {
        this.errored = true;
        this.ev.onError?.(e);
      }
      return [];
    });
  }

  /** pump starts the requests inside the window: the current sentence and AHEAD after it. */
  private pump(): void {
    for (let i = this.at; i < this.items.length && i <= this.at + AHEAD; i++)
      this.startFetch(this.items[i]);
  }

  private async run(): Promise<void> {
    if (this.running) return;
    this.running = true;
    try {
      while (!this.stopped) {
        if (this.at >= this.items.length) {
          if (this.ended) {
            this.resolveDone();
          }
          return;
        }
        this.pump();
        const it = this.items[this.at];
        const wavs = it.wavs ? await it.wavs : null;
        if (this.stopped) return;
        if (wavs === null) {
          if (!this.textOnly) {
            this.textOnly = true;
            this.ev.onTextOnly?.();
          }
        } else {
          for (const w of wavs) {
            if (this.stopped) return;
            this.cache.push(w);
            if (!this.spoke) {
              this.spoke = true;
              this.ev.onSpeak?.();
            }
            await this.deps.play(w, this.ctl.signal);
          }
        }
        this.at++;
        this.pump();
      }
    } finally {
      this.running = false;
    }
  }
}

/**
 * replay plays cached WAVs again, in order, with no request (Repeat). Resolves when the last one
 * ended, or when signal aborted.
 */
export async function replay(
  wavs: readonly Blob[],
  play: TtsDeps["play"],
  signal: AbortSignal,
): Promise<void> {
  for (const w of wavs) {
    if (signal.aborted) return;
    await play(w, signal);
  }
}
