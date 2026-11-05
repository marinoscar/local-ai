import { SseEventTooLargeError, SseSplitter } from './sse-splitter';

const TERMINATORS = ['\n\n', '\r\n\r\n', '\n\r\n', '\r\n\n'];

/** A small deterministic generator (mulberry32): the fuzz runs are reproducible. */
function prng(seed: number): () => number {
  let state = seed >>> 0;
  return () => {
    state = (state + 0x6d2b79f5) >>> 0;
    let t = state;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const PAYLOADS = ['{"choices":[{"delta":{"content":"hola"}}]}', '{"choices":[{"delta":{"content":"café € 😀"}}]}', '[DONE]', '{"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2}}'];

/** `count` events with random line ends, and the same events as a list of buffers. */
function makeStream(random: () => number, count: number): { events: Buffer[]; bytes: Buffer } {
  const events: Buffer[] = [];
  for (let index = 0; index < count; index += 1) {
    const terminator = TERMINATORS[Math.floor(random() * TERMINATORS.length)]!;
    const lines = 1 + Math.floor(random() * 2);
    let text = '';
    for (let line = 0; line < lines; line += 1) {
      const lineEnd = line < lines - 1 ? (random() < 0.5 ? '\n' : '\r\n') : '';
      text += `data: ${PAYLOADS[Math.floor(random() * PAYLOADS.length)]!}${lineEnd}`;
    }
    events.push(Buffer.from(text + terminator, 'utf8'));
  }
  return { events, bytes: Buffer.concat(events) };
}

function feed(parts: Buffer[]): { events: Buffer[]; held: number } {
  const splitter = new SseSplitter();
  const events: Buffer[] = [];
  for (const part of parts) events.push(...splitter.push(part));
  return { events, held: splitter.heldBytes };
}

const asText = (events: Buffer[]): string[] => events.map((event) => event.toString('utf8'));

describe('SseSplitter', () => {
  it('returns each complete event with its blank line, and nothing for a partial one', () => {
    const splitter = new SseSplitter();
    expect(asText(splitter.push(Buffer.from('data: a\n\ndata: b\n\ndata: c')))).toEqual(['data: a\n\n', 'data: b\n\n']);
    expect(splitter.heldBytes).toBe('data: c'.length);
    expect(asText(splitter.push(Buffer.from('\n\n')))).toEqual(['data: c\n\n']);
    expect(splitter.heldBytes).toBe(0);
  });

  it.each(TERMINATORS)('splits on the terminator %j', (terminator) => {
    const input = Buffer.from(`data: one${terminator}data: two${terminator}`);
    expect(asText(feed([input]).events)).toEqual([`data: one${terminator}`, `data: two${terminator}`]);
  });

  it('recognises the blank line when its \\r and \\n arrive in different chunks', () => {
    const { events, held } = feed([Buffer.from('data: x\r'), Buffer.from('\n'), Buffer.from('\r'), Buffer.from('\n')]);
    expect(asText(events)).toEqual(['data: x\r\n\r\n']);
    expect(held).toBe(0);
  });

  it('keeps [DONE] as an ordinary event', () => {
    expect(asText(feed([Buffer.from('data: {"a":1}\n\ndata: [DONE]\n\n')]).events)).toEqual(['data: {"a":1}\n\n', 'data: [DONE]\n\n']);
  });

  it('does not treat a comment or an "event:" line as an event end', () => {
    expect(asText(feed([Buffer.from(': keep-alive\nevent: ping\ndata: x\n\n')]).events)).toEqual([': keep-alive\nevent: ping\ndata: x\n\n']);
  });

  it('hands out an event that sits inside one chunk without copying it', () => {
    const chunk = Buffer.from('data: a\n\ndata: b\n\n');
    const [first] = new SseSplitter().push(chunk);
    expect(first!.buffer).toBe(chunk.buffer);
  });

  it('holds only the partial tail: a copy of exactly those bytes', () => {
    const splitter = new SseSplitter();
    const events = splitter.push(Buffer.from('data: whole\n\ndata: par'));
    expect(events).toHaveLength(1);
    expect(splitter.heldBytes).toBe('data: par'.length);
  });

  it('keeps the bytes of a multi-byte character that is split across chunks until the event is complete', () => {
    const bytes = Buffer.from('data: café\n\n', 'utf8');
    const cut = bytes.indexOf(0xc3) + 1;
    const { events } = feed([bytes.subarray(0, cut), bytes.subarray(cut)]);
    expect(events).toHaveLength(1);
    expect(events[0]!.toString('utf8')).toBe('data: café\n\n');
  });

  it('refuses an event that never ends, once it passes the limit', () => {
    const splitter = new SseSplitter({ maxEventBytes: 64 });
    splitter.push(Buffer.alloc(40, 0x61));
    expect(() => splitter.push(Buffer.alloc(40, 0x61))).toThrow(SseEventTooLargeError);
  });

  it('refuses a single oversized chunk without a blank line', () => {
    expect(() => new SseSplitter({ maxEventBytes: 64 }).push(Buffer.alloc(100, 0x61))).toThrow(SseEventTooLargeError);
  });

  it('accepts a long run of events: the limit is per event, not per stream', () => {
    const splitter = new SseSplitter({ maxEventBytes: 64 });
    for (let index = 0; index < 200; index += 1) expect(splitter.push(Buffer.from('data: abcdef\n\n'))).toHaveLength(1);
  });
});

describe('SseSplitter fuzz', () => {
  it('yields the same events however the bytes are cut: every single cut offset', () => {
    for (let seed = 1; seed <= 12; seed += 1) {
      const { events, bytes } = makeStream(prng(seed), 6);
      for (let cut = 0; cut <= bytes.length; cut += 1) {
        const result = feed([bytes.subarray(0, cut), bytes.subarray(cut)]);
        expect(asText(result.events)).toEqual(asText(events));
        expect(result.held).toBe(0);
      }
    }
  });

  it('yields the same events for every pair of cut offsets', () => {
    const { events, bytes } = makeStream(prng(99), 3);
    for (let first = 0; first <= bytes.length; first += 1) {
      for (let second = first; second <= bytes.length; second += 1) {
        const result = feed([bytes.subarray(0, first), bytes.subarray(first, second), bytes.subarray(second)]);
        expect(asText(result.events)).toEqual(asText(events));
      }
    }
  });

  it('yields the same events one byte at a time, and with random chunking of long streams', () => {
    for (let seed = 100; seed < 120; seed += 1) {
      const random = prng(seed);
      const { events, bytes } = makeStream(random, 40);
      expect(asText(feed([...bytes].map((byte) => Buffer.from([byte]))).events)).toEqual(asText(events));

      const parts: Buffer[] = [];
      for (let at = 0; at < bytes.length; ) {
        const size = 1 + Math.floor(random() * 90);
        parts.push(bytes.subarray(at, at + size));
        at += size;
      }
      expect(asText(feed(parts).events)).toEqual(asText(events));
    }
  });

  it('holds exactly the unfinished tail at every cut, and the events plus the tail are the input', () => {
    const { bytes } = makeStream(prng(7), 5);
    const unfinished = Buffer.from('data: {"partial":tr');
    const input = Buffer.concat([bytes, unfinished]);
    for (let cut = 0; cut <= input.length; cut += 1) {
      const splitter = new SseSplitter();
      const emitted = [...splitter.push(input.subarray(0, cut)), ...splitter.push(input.subarray(cut))];
      expect(Buffer.concat(emitted).length + splitter.heldBytes).toBe(input.length);
      expect(splitter.heldBytes).toBe(unfinished.length);
    }
  });
});
