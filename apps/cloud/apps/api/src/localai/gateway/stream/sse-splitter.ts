// Splits a byte stream of Server-Sent Events into complete events (docs/specs/gateway.md section 4.2, Stream, step 2).
//
// An event ends at a blank line: `\n\n`, `\r\n\r\n` or a mix of the two (`\n\r\n`, `\r\n\n`). `push()` returns every event
// the new bytes completed, each INCLUDING its terminating blank line, so the caller writes it to the client as it is, byte
// for byte. Only the partial tail of an unfinished event is kept between calls (a copy of just those bytes, never the
// whole chunk); everything else is returned as a view of the chunk it came in, with no copy.
//
// Bytes, not text: a multi-byte character split across two chunks stays split until the event is complete, and nothing is
// ever decoded and re-encoded. A bare `\r` as a line end (allowed by the SSE standard, never sent by llama-server) is not
// a line terminator here.

/** An unfinished event grew past the limit without a blank line: the node is not speaking SSE. */
export class SseEventTooLargeError extends Error {
  constructor(readonly limit: number) {
    super('An SSE event is larger than the gateway accepts.');
    this.name = 'SseEventTooLargeError';
  }
}

/** One event of a chat or completion chunk is a few hundred bytes; 1 MiB is far beyond any real one. */
export const DEFAULT_MAX_EVENT_BYTES = 1024 * 1024;

const LF = 0x0a;
const CR = 0x0d;

export interface SseSplitterOptions {
  /** The most bytes one event may take before its blank line arrives. */
  maxEventBytes?: number;
}

export class SseSplitter {
  private readonly maxEventBytes: number;
  /** Bytes of the current unfinished event that arrived in earlier chunks. */
  private pending: Buffer[] = [];
  private pendingBytes = 0;
  /** The current line so far: its length and its first byte (a line that is exactly `\r` is blank). */
  private lineLength = 0;
  private lineFirstByte = 0;

  constructor(options: SseSplitterOptions = {}) {
    this.maxEventBytes = options.maxEventBytes ?? DEFAULT_MAX_EVENT_BYTES;
  }

  /** How many bytes of an unfinished event are being held. */
  get heldBytes(): number {
    return this.pendingBytes;
  }

  /**
   * Feeds the next bytes. Returns the events they completed, in order.
   * @throws SseEventTooLargeError the unfinished event exceeds `maxEventBytes`. The splitter must not be used afterwards.
   */
  push(chunk: Buffer): Buffer[] {
    const events: Buffer[] = [];
    let eventStart = 0;
    let lineStart = 0;
    let newline = chunk.indexOf(LF, 0);

    while (newline !== -1) {
      this.extendLine(chunk, lineStart, newline);
      const blank = this.lineLength === 0 || (this.lineLength === 1 && this.lineFirstByte === CR);
      this.lineLength = 0;
      lineStart = newline + 1;
      if (blank) {
        events.push(this.take(chunk, eventStart, lineStart));
        eventStart = lineStart;
      }
      newline = chunk.indexOf(LF, lineStart);
    }

    this.extendLine(chunk, lineStart, chunk.length);
    if (eventStart < chunk.length) {
      const tail = chunk.subarray(eventStart);
      if (this.pendingBytes + tail.length > this.maxEventBytes) throw new SseEventTooLargeError(this.maxEventBytes);
      // A copy: the tail must not keep the whole chunk alive.
      this.pending.push(Buffer.from(tail));
      this.pendingBytes += tail.length;
    }
    return events;
  }

  /** Drops the held bytes (the stream ended or failed). */
  reset(): void {
    this.pending = [];
    this.pendingBytes = 0;
    this.lineLength = 0;
    this.lineFirstByte = 0;
  }

  private extendLine(chunk: Buffer, from: number, to: number): void {
    if (to <= from) return;
    if (this.lineLength === 0) this.lineFirstByte = chunk[from]!;
    this.lineLength += to - from;
  }

  /** The finished event: the held bytes of earlier chunks, then `chunk[from, to)`. */
  private take(chunk: Buffer, from: number, to: number): Buffer {
    const part = chunk.subarray(from, to);
    if (this.pending.length === 0) return part;
    if (this.pendingBytes + part.length > this.maxEventBytes) throw new SseEventTooLargeError(this.maxEventBytes);
    const event = Buffer.concat([...this.pending, part], this.pendingBytes + part.length);
    this.pending = [];
    this.pendingBytes = 0;
    return event;
  }
}
