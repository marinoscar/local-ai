// The stream proxy against REAL local HTTP servers acting as llama-servers (docs/specs/gateway.md section 4.2, Stream): the
// gateway scope listens on a real port and the specs read it with a plain `http.request`, because only a real socket shows
// whether an event arrives the moment it is complete. Covers: no buffering, retries up to the first byte and none after,
// the mid-stream error event, client abort reaching the node, the idle and total timers, the usage-only chunk, framing,
// backpressure, and that every request is settled exactly once.
import { Logger } from '@nestjs/common';

import { MIDSTREAM_ERROR_EVENT } from '../../src/localai/gateway/stream/stream-proxy';
import { respondJson } from '../fakes/fake-llama';
import { chatChunk, drained, emit, sleep, sseBehaviour, sseHeaders, usageChunk, type SseScript } from '../fakes/fake-llama-sse';
import { CHAT, streamLab, waitFor, type StreamClient, type StreamLab } from './support/stream-lab';

beforeAll(() => {
  jest.spyOn(Logger.prototype, 'debug').mockImplementation(() => undefined);
  jest.spyOn(Logger.prototype, 'warn').mockImplementation(() => undefined);
});
afterAll(() => jest.restoreAllMocks());

const labs: StreamLab[] = [];
afterEach(async () => {
  await Promise.all(labs.splice(0).map((entry) => entry.cleanup()));
});
const setup = async (...args: Parameters<typeof streamLab>): Promise<StreamLab> => {
  const created = await streamLab(...args);
  labs.push(created);
  return created;
};

/** The exact bytes of spec section 3.3, typed out here on purpose: the constant in the source must equal them. */
const SPEC_MIDSTREAM_ERROR =
  'data: {"error":{"message":"The node stopped answering in the middle of the reply.","type":"server_error","code":"upstream_interrupted","localai_code":"LAI-GW-045"}}\n\n' + 'data: [DONE]\n\n';

const contentOf = (client: StreamClient): string[] =>
  client.payloads.filter((payload) => payload.startsWith('{')).flatMap((payload) => {
    const parsed = JSON.parse(payload) as { choices?: Array<{ delta?: { content?: string | null }; text?: string }> };
    const value = parsed.choices?.[0]?.delta?.content ?? parsed.choices?.[0]?.text;
    return typeof value === 'string' && value !== '' ? [value] : [];
  });

/** How long after the node wrote each event the client had it. Events are matched by their text, in order. */
function latencies(client: StreamClient, script: SseScript): number[] {
  const out: number[] = [];
  let pointer = 0;
  for (const event of client.events) {
    while (pointer < script.emitted!.length && script.emitted![pointer] !== event.text) pointer += 1;
    expect(pointer).toBeLessThan(script.emitted!.length);
    out.push(event.at - script.emittedAt![pointer]!);
  }
  return out;
}

/** Waits until the request was settled and returns its one usage record. */
async function settledRecord(l: StreamLab): Promise<StreamLab['sink']['records'][number]> {
  expect(await waitFor(() => l.sink.records.length > 0)).toBe(true);
  // Anything that would settle twice has had time to do so by now.
  await sleep(50);
  expect(l.sink.records).toHaveLength(1);
  expect(l.limits.settled).toHaveLength(1);
  return l.sink.records[0]!;
}

describe('stream proxy: passthrough', () => {
  it('delivers every event within 20 ms of the node writing it (nothing is buffered)', async () => {
    const script: SseScript = { tokens: ['Ho', 'la', ' mu', 'ndo', '!', ' ', 'Que', ' ta', 'l', '?'], intervalMs: 50 };
    const l = await setup([{ id: 'ai-n1', behaviour: sseBehaviour(script) }]);
    const client = l.open('/chat/completions', CHAT);
    await client.done;

    expect(client.status).toBe(200);
    expect(contentOf(client).join('')).toBe('Hola mundo! Que tal?');
    const delays = latencies(client, script);
    expect(delays.length).toBe(client.events.length);
    expect(Math.max(...delays)).toBeLessThan(20);
    // The first event reached the client long before the node had written the last one.
    expect(client.events[0]!.at).toBeLessThan(script.emittedAt![script.emittedAt!.length - 1]!);
    expect(client.ended).toBe(true);
  });

  it('sets the stream headers and carries the pipeline headers over to the hijacked response', async () => {
    const l = await setup([{ id: 'ai-n1', behaviour: sseBehaviour({ tokens: ['a'] }) }]);
    l.gw.config.origins = ['https://app.example.test'];
    const client = l.open('/chat/completions', CHAT, { headers: { origin: 'https://app.example.test', 'x-request-id': 'req-stream-1' } });
    await client.done;
    expect(client.status).toBe(200);
    expect(client.headers['content-type']).toBe('text/event-stream; charset=utf-8');
    expect(client.headers['cache-control']).toBe('no-cache');
    expect(client.headers['x-accel-buffering']).toBe('no');
    expect(client.headers['x-request-id']).toBe('req-stream-1');
    expect(client.headers['x-localai-node']).toBe('ai-n1');
    expect(client.headers['x-localai-attempts']).toBe('1');
    expect(client.headers['x-ratelimit-limit-requests']).toBe('60');
    expect(client.headers['access-control-allow-origin']).toBe('https://app.example.test');
    expect(client.headers['content-encoding']).toBeUndefined();
    expect(client.headers['content-length']).toBeUndefined();
  });

  it('passes the events through byte for byte, with CRLF framing too, ending with [DONE]', async () => {
    for (const crlf of [false, true]) {
      const script: SseScript = { tokens: ['x', 'y'], crlf };
      const l = await setup([{ id: 'ai-n1', behaviour: sseBehaviour(script) }]);
      const client = l.open('/chat/completions', CHAT);
      await client.done;
      // The usage-only chunk is the one event the client did not ask for.
      const expected = script.emitted!.filter((text) => !text.includes('"choices":[]'));
      expect(client.texts).toEqual(expected);
      expect(client.payloads.at(-1)).toBe('[DONE]');
    }
  });

  it('sends the node the rewritten body and an event-stream Accept, and keeps the client stream_options', async () => {
    const l = await setup([{ id: 'ai-n1', behaviour: sseBehaviour({ tokens: ['a'] }) }]);
    const client = l.open('/chat/completions', { ...CHAT, model: 'lab-model', stream_options: { include_obfuscation: false } });
    await client.done;
    const seen = l.llamas['ai-n1']!.requests[0]!;
    expect(seen.headers.accept).toBe('text/event-stream');
    expect(JSON.parse(seen.body)).toEqual({ ...CHAT, model: 'model-a', stream_options: { include_obfuscation: false, include_usage: true } });
  });

  it('holds an event back until it is complete, then sends it whole', async () => {
    const half = chatChunk('Hola');
    const l = await setup([
      {
        id: 'ai-n1',
        behaviour: async (_req, res) => {
          sseHeaders(res);
          res.write(`data: ${half.slice(0, 20)}`);
          await sleep(150);
          res.write(`${half.slice(20)}\n`);
          await sleep(50);
          res.write('\n');
          await sleep(50);
          res.write('data: [DONE]\n\n');
          res.end();
        },
      },
    ]);
    const client = l.open('/chat/completions', CHAT);
    await client.head;
    await sleep(100);
    expect(client.bytes).toBe(0);
    await client.done;
    expect(client.texts).toEqual([`data: ${half}\n\n`, 'data: [DONE]\n\n']);
  });

  it('retries on the next node while nothing was sent, and reports the attempts', async () => {
    const l = await setup([{ id: 'ai-n1', behaviour: (_r, res) => respondJson(res, 503, { error: { message: 'loading' } }) }, { id: 'ai-n2', behaviour: sseBehaviour({ tokens: ['ok'] }) }]);
    const client = l.open('/chat/completions', CHAT);
    await client.done;
    expect(client.status).toBe(200);
    expect(client.headers['x-localai-node']).toBe('ai-n2');
    expect(client.headers['x-localai-attempts']).toBe('2');
    expect(contentOf(client)).toEqual(['ok']);
    expect(l.table.isSuspect('ai-n1')).toBe(true);
    expect(l.table.inflightOf({ nodeId: 'ai-n1', modelId: 'model-a' })).toBe(0);
  });

  it('answers a rejected stream request with the ordinary JSON error (nothing was sent yet)', async () => {
    const l = await setup([{ id: 'ai-n1', behaviour: (_r, res) => respondJson(res, 400, { error: { code: 400, message: 'prompt too long', type: 'invalid_request_error' } }) }]);
    const client = l.open('/chat/completions', CHAT);
    await client.done;
    expect(client.status).toBe(400);
    expect(client.headers['content-type']).toContain('application/json');
    expect(client.json.error).toMatchObject({ localai_code: 'LAI-GW-042', message: 'prompt too long' });
    expect(await settledRecord(l)).toMatchObject({ status: 400, errorCode: 'LAI-GW-042', stream: true });
  });

  it('answers 502 LAI-GW-040 when no node can be reached', async () => {
    const l = await setup([{ id: 'ai-n1' }]);
    await l.llamas['ai-n1']!.close();
    const client = l.open('/chat/completions', CHAT);
    await client.done;
    expect(client.status).toBe(502);
    expect(client.json.error.localai_code).toBe('LAI-GW-040');
    expect(await settledRecord(l)).toMatchObject({ status: 502, errorCode: 'LAI-GW-040', stream: true });
  });

  it('writes the request log line when the stream ends (Fastify onResponse still runs after hijack)', async () => {
    const l = await setup([{ id: 'ai-n1', behaviour: sseBehaviour({ tokens: ['a', 'b'] }) }]);
    const client = l.open('/chat/completions', CHAT);
    await client.done;
    expect(await waitFor(() => l.gw.logs.info.length > 0)).toBe(true);
    const line = JSON.parse(l.gw.logs.info[0]!) as Record<string, unknown>;
    expect(line).toMatchObject({ endpoint: 'chat.completions', status: 200, stream: true, aborted: false, node_id: 'ai-n1', prompt_tokens: 11, completion_tokens: 2, usage_source: 'upstream' });
    expect(l.gw.logs.info[0]).not.toContain('Hola');
  });
});

describe('stream proxy: usage', () => {
  it('records the usage of the usage-only chunk and drops that chunk when the client did not ask for it', async () => {
    const l = await setup([{ id: 'ai-n1', behaviour: sseBehaviour({ tokens: ['a', 'b', 'c'], promptTokens: 17 }) }]);
    const client = l.open('/chat/completions', CHAT);
    await client.done;
    expect(client.body).not.toContain('"usage"');
    expect(client.body).not.toContain('"choices":[]');
    const record = await settledRecord(l);
    expect(record).toMatchObject({
      endpoint: 'chat.completions',
      model: 'model-a',
      nodeId: 'ai-n1',
      status: 200,
      errorCode: null,
      stream: true,
      aborted: false,
      attempts: 1,
      promptTokens: 17,
      completionTokens: 3,
      usageSource: 'upstream',
    });
    expect(record.ttfbMs).toEqual(expect.any(Number));
    expect(l.limits.settled).toEqual([{ promptTokens: 17, completionTokens: 3, aborted: false }]);
    expect(l.table.inflightOf({ nodeId: 'ai-n1', modelId: 'model-a' })).toBe(0);
  });

  it('forwards the usage-only chunk when the client asked for it', async () => {
    const l = await setup([{ id: 'ai-n1', behaviour: sseBehaviour({ tokens: ['a', 'b'], promptTokens: 9 }) }]);
    const client = l.open('/chat/completions', { ...CHAT, stream_options: { include_usage: true } });
    await client.done;
    const usageEvents = client.payloads.filter((payload) => payload.includes('"usage"'));
    expect(usageEvents).toEqual([usageChunk(9, 2)]);
    expect(await settledRecord(l)).toMatchObject({ promptTokens: 9, completionTokens: 2, usageSource: 'upstream' });
  });

  it('estimates the usage of a completions stream (no usage chunk is requested there)', async () => {
    const l = await setup([{ id: 'ai-n1', behaviour: sseBehaviour({ tokens: ['a', 'b', 'c', 'd'] }) }]);
    const client = l.open('/completions', { model: 'model-a', stream: true, prompt: 'x' });
    await client.done;
    expect(JSON.parse(l.llamas['ai-n1']!.requests[0]!.body)).not.toHaveProperty('stream_options');
    expect(contentOf(client)).toEqual(['a', 'b', 'c', 'd']);
    expect(await settledRecord(l)).toMatchObject({ endpoint: 'completions', promptTokens: null, completionTokens: 4, usageSource: 'estimated', status: 200, errorCode: null });
  });
});

describe('stream proxy: failures after the first byte', () => {
  it('is the exact spec event, so the source constant equals the spec text', () => {
    expect(MIDSTREAM_ERROR_EVENT.toString('utf8')).toBe(SPEC_MIDSTREAM_ERROR);
  });

  it('ends with the error event and [DONE] when the node dies mid-stream, without retrying', async () => {
    const l = await setup([
      { id: 'ai-n1', behaviour: sseBehaviour({ tokens: ['a', 'b', 'c', 'd'], intervalMs: 10, after: { tokens: 2, action: 'destroy' } }) },
      { id: 'ai-n2', behaviour: sseBehaviour({ tokens: ['never'] }) },
    ]);
    const client = l.open('/chat/completions', CHAT);
    await client.done;
    expect(client.status).toBe(200);
    expect(contentOf(client)).toEqual(['a', 'b']);
    expect(client.texts.slice(-2).join('')).toBe(SPEC_MIDSTREAM_ERROR);
    expect(client.ended).toBe(true);
    expect(l.llamas['ai-n2']!.requests).toHaveLength(0);
    expect(client.headers['x-localai-attempts']).toBe('1');

    const record = await settledRecord(l);
    expect(record).toMatchObject({ status: 200, errorCode: 'LAI-GW-045', aborted: false, nodeId: 'ai-n1', attempts: 1, promptTokens: null, completionTokens: 2, usageSource: 'estimated' });
    expect(l.table.inflightOf({ nodeId: 'ai-n1', modelId: 'model-a' })).toBe(0);
  });

  it('treats a body that ends without [DONE] as an interrupted reply', async () => {
    const l = await setup([{ id: 'ai-n1', behaviour: sseBehaviour({ tokens: ['a', 'b', 'c'], after: { tokens: 3, action: 'end' } }) }]);
    const client = l.open('/chat/completions', CHAT);
    await client.done;
    expect(contentOf(client)).toEqual(['a', 'b', 'c']);
    expect(client.texts.slice(-2).join('')).toBe(SPEC_MIDSTREAM_ERROR);
    expect(await settledRecord(l)).toMatchObject({ status: 200, errorCode: 'LAI-GW-045', usageSource: 'estimated', completionTokens: 3 });
  });

  it('treats an event that never ends as an interrupted reply', async () => {
    const l = await setup(
      [
        {
          id: 'ai-n1',
          behaviour: async (_req, res) => {
            sseHeaders(res);
            await emit(res, {} as SseScript, chatChunk('fine'));
            res.write(`data: ${'x'.repeat(4096)}`);
            await sleep(500);
            res.end();
          },
        },
      ],
      undefined,
      { maxEventBytes: 1024 },
    );
    const client = l.open('/chat/completions', CHAT);
    await client.done;
    expect(contentOf(client)).toEqual(['fine']);
    expect(client.texts.slice(-2).join('')).toBe(SPEC_MIDSTREAM_ERROR);
    expect(await settledRecord(l)).toMatchObject({ errorCode: 'LAI-GW-045' });
  });

  it('ends with the error event after the idle time, and drops the node connection', async () => {
    const l = await setup([{ id: 'ai-n1', behaviour: sseBehaviour({ tokens: ['a', 'b', 'c'], after: { tokens: 2, action: 'stall' } }) }], (s) => (s.timeouts.idleMs = 400));
    const startedAt = Date.now();
    const client = l.open('/chat/completions', CHAT);
    await client.done;
    const took = Date.now() - startedAt;
    expect(contentOf(client)).toEqual(['a', 'b']);
    expect(client.texts.slice(-2).join('')).toBe(SPEC_MIDSTREAM_ERROR);
    expect(client.ended).toBe(true);
    expect(took).toBeGreaterThanOrEqual(380);
    expect(took).toBeLessThan(2000);
    expect(await waitFor(() => l.llamas['ai-n1']!.abortedAt.length === 1)).toBe(true);
    expect(await settledRecord(l)).toMatchObject({ status: 200, errorCode: 'LAI-GW-044', aborted: false, completionTokens: 2, usageSource: 'estimated' });
  });

  it('ends with the error event after the total time, however lively the node is', async () => {
    const l = await setup([{ id: 'ai-n1', behaviour: sseBehaviour({ tokens: ['t'], intervalMs: 30, endless: true }) }], (s) => (s.timeouts.totalMs = 600));
    const startedAt = Date.now();
    const client = l.open('/chat/completions', CHAT);
    await client.done;
    const took = Date.now() - startedAt;
    expect(client.texts.slice(-2).join('')).toBe(SPEC_MIDSTREAM_ERROR);
    expect(client.ended).toBe(true);
    expect(took).toBeGreaterThanOrEqual(550);
    expect(took).toBeLessThan(2000);
    expect(contentOf(client).length).toBeGreaterThan(5);
    expect(await waitFor(() => l.llamas['ai-n1']!.abortedAt.length === 1, 1000)).toBe(true);
    expect(await settledRecord(l)).toMatchObject({ status: 200, errorCode: 'LAI-GW-044', aborted: false, usageSource: 'estimated' });
  });

  it('answers 504 LAI-GW-044 (plain JSON) when the total time runs out before the first byte', async () => {
    const l = await setup([{ id: 'ai-n1', behaviour: async (_req, res) => { await sleep(1500); res.destroy(); } }], (s) => (s.timeouts.totalMs = 300));
    const client = l.open('/chat/completions', CHAT);
    await client.done;
    expect(client.status).toBe(504);
    expect(client.json.error.localai_code).toBe('LAI-GW-044');
    expect(await settledRecord(l)).toMatchObject({ status: 504, errorCode: 'LAI-GW-044', stream: true });
  });
});

describe('stream proxy: client abort', () => {
  it('closes the node connection within 200 ms and records the request as aborted', async () => {
    const l = await setup([{ id: 'ai-n1', behaviour: sseBehaviour({ tokens: ['t'], intervalMs: 20, endless: true }) }]);
    const client = l.open('/chat/completions', CHAT);
    expect(await waitFor(() => client.events.length >= 4)).toBe(true);
    const abortedAt = Date.now();
    client.abort();
    expect(await waitFor(() => l.llamas['ai-n1']!.abortedAt.length === 1, 1000)).toBe(true);
    expect(l.llamas['ai-n1']!.abortedAt[0]! - abortedAt).toBeLessThan(200);

    const record = await settledRecord(l);
    expect(record).toMatchObject({ status: 200, errorCode: null, aborted: true, nodeId: 'ai-n1', stream: true, promptTokens: null, usageSource: 'estimated' });
    expect(record.completionTokens).toBeGreaterThanOrEqual(3);
    expect(l.limits.settled[0]).toMatchObject({ aborted: true });
    expect(l.table.inflightOf({ nodeId: 'ai-n1', modelId: 'model-a' })).toBe(0);
  });

  it('aborts the node call when the client leaves before the node has answered', async () => {
    const l = await setup([{ id: 'ai-n1', behaviour: async () => { await new Promise<void>(() => undefined); } }]);
    const client = l.open('/chat/completions', CHAT);
    expect(await waitFor(() => l.llamas['ai-n1']!.requests.length === 1)).toBe(true);
    const abortedAt = Date.now();
    client.abort();
    expect(await waitFor(() => l.llamas['ai-n1']!.abortedAt.length === 1, 1000)).toBe(true);
    expect(l.llamas['ai-n1']!.abortedAt[0]! - abortedAt).toBeLessThan(200);
    expect(await settledRecord(l)).toMatchObject({ status: 499, aborted: true, errorCode: null, usageSource: 'none' });
    expect(l.table.inflightOf({ nodeId: 'ai-n1', modelId: 'model-a' })).toBe(0);
  });
});

describe('stream proxy: backpressure', () => {
  it('pauses the node while the client does not read, keeps memory bounded, and delivers everything once it reads again', async () => {
    const payload = JSON.stringify({ choices: [{ index: 0, delta: { content: 'z'.repeat(64 * 1024) } }] });
    const eventBytes = Buffer.byteLength(`data: ${payload}\n\n`);
    const OFFERED = 512; // events of 64 KiB: 32 MiB offered, far more than the socket buffers on the way can hold
    const node = { written: 0, blocked: 0, finished: false };
    const l = await setup(
      [
        {
          id: 'ai-n1',
          behaviour: async (_req, res) => {
            sseHeaders(res);
            for (let index = 0; index < OFFERED && !res.destroyed; index += 1) {
              const ok = res.write(`data: ${payload}\n\n`);
              node.written += 1;
              if (!ok) {
                node.blocked += 1;
                await drained(res);
              }
            }
            res.write('data: [DONE]\n\n');
            res.end();
            node.finished = true;
          },
        },
      ],
      (s) => (s.timeouts.idleMs = 700),
    );

    const client = l.open('/chat/completions', CHAT, { pauseAfterHead: true, retain: false });
    await client.head;
    await sleep(1500);
    const stalledAt = node.written;
    await sleep(500);
    // The node was told to wait (`write()` returned false) and made no progress while the client read nothing.
    expect(node.blocked).toBeGreaterThanOrEqual(1);
    expect(node.written).toBe(stalledAt);
    expect(node.finished).toBe(false);
    // What sits in the socket buffers on the way is a small part of what was on offer.
    expect(node.written * eventBytes).toBeLessThan((OFFERED * eventBytes) / 2);
    // The stalled client did not trip the idle timer: the node, not the client, is what that timer watches.
    expect(client.ended).toBe(false);

    client.resume();
    await client.done;
    expect(node.finished).toBe(true);
    expect(client.ended).toBe(true);
    expect(client.eventCount).toBe(OFFERED + 1);
    expect(client.bytes).toBe(OFFERED * eventBytes + 'data: [DONE]\n\n'.length);
    expect(await settledRecord(l)).toMatchObject({ status: 200, errorCode: null, aborted: false });
  }, 30_000);

  it('releases a stalled client when it leaves, and the node connection with it', async () => {
    const payload = JSON.stringify({ choices: [{ index: 0, delta: { content: 'z'.repeat(64 * 1024) } }] });
    const l = await setup([
      {
        id: 'ai-n1',
        behaviour: async (_req, res) => {
          sseHeaders(res);
          for (let index = 0; index < 2000 && !res.destroyed; index += 1) {
            if (!res.write(`data: ${payload}\n\n`)) await drained(res);
          }
          res.end();
        },
      },
    ]);
    const client = l.open('/chat/completions', CHAT, { pauseAfterHead: true, retain: false });
    await client.head;
    await sleep(800);
    client.abort();
    expect(await waitFor(() => l.llamas['ai-n1']!.abortedAt.length === 1, 2000)).toBe(true);
    expect(await settledRecord(l)).toMatchObject({ aborted: true, status: 200 });
    expect(l.table.inflightOf({ nodeId: 'ai-n1', modelId: 'model-a' })).toBe(0);
  });
});

describe('stream proxy: sanity', () => {
  it('keeps serving: a second stream after an aborted one is fine, and node connections are reused after a clean end', async () => {
    const l = await setup([{ id: 'ai-n1', behaviour: sseBehaviour({ tokens: ['a', 'b'] }) }]);
    const first = l.open('/chat/completions', CHAT);
    await first.done;
    const second = l.open('/chat/completions', CHAT);
    await second.done;
    expect(contentOf(second)).toEqual(['a', 'b']);
    expect(l.sink.records).toHaveLength(2);
    // The node saw no connection closed on it while a request was still open.
    expect(l.llamas['ai-n1']!.abortedAt).toHaveLength(0);
  });
});
