import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import {
  AgentClient,
  AgentFeed,
  AgentTokenError,
  type AgentEvent,
  type ConnectionState,
  SseParser,
  TOKEN_PATH,
  checkApiVersion,
  frameToEvent,
  resolveTokenPath,
  testHooksAllowed,
} from '../../src/main/agent-client';
import { type LogEntry, type Logger } from '../../src/main/log';
import { type FakeAgent, newFakeToken, startFakeAgent } from '../fixtures/fake-agent';

let agent: FakeAgent;
let dir: string;
let tokenPath: string;
let lines: LogEntry[];
const log: Logger = {
  write: (entry) => void lines.push(entry),
  log: (level, component, msg, fields = {}) => void lines.push({ ...fields, ts: '', level, msg, component }),
};

beforeEach(async () => {
  lines = [];
  agent = await startFakeAgent({ port: 0 });
  dir = await mkdtemp(join(tmpdir(), 'ai-desktop-client-'));
  tokenPath = join(dir, 'agent-token');
  await writeFile(tokenPath, `${agent.token}\n`);
});
afterEach(async () => {
  await agent.close();
  await rm(dir, { recursive: true, force: true });
});

const client = (over: ConstructorParameters<typeof AgentClient>[0] = {}): AgentClient => new AgentClient({ baseUrl: agent.url, tokenPath, log, sleep: tick, ...over });
/** A sleep that costs no time but yields to the event loop (a bare microtask would starve the timers of `until`). */
const tick = (): Promise<void> => new Promise((r) => setTimeout(r, 1));
const until = async (condition: () => boolean, ms = 3000): Promise<void> => {
  const end = Date.now() + ms;
  while (!condition()) {
    if (Date.now() > end) throw new Error('timed out waiting for a condition');
    await new Promise((r) => setTimeout(r, 5));
  }
};

describe('version and paths', () => {
  it('compares the major of /health.version (LAI-DSK-006)', () => {
    expect(checkApiVersion('0.1.0')).toBe('ok');
    expect(checkApiVersion('0.0.0-dev')).toBe('ok');
    expect(checkApiVersion('1.0.0')).toBe('mismatch');
    expect(checkApiVersion('2.3.4', 2)).toBe('ok');
    expect(checkApiVersion('garbage')).toBe('mismatch');
    expect(checkApiVersion(undefined)).toBe('mismatch');
  });

  it('honours the token path override only where test hooks are allowed', () => {
    const env = { LOCALAI_DESKTOP_TOKEN_PATH: '/tmp/x/token' };
    expect(resolveTokenPath(env, true)).toBe('/tmp/x/token');
    expect(resolveTokenPath(env, false)).toBe(TOKEN_PATH);
    expect(resolveTokenPath({ LOCALAI_DESKTOP_TOKEN_PATH: 'relative' }, true)).toBe(TOKEN_PATH);
    expect(resolveTokenPath({}, true)).toBe(TOKEN_PATH);
    expect(testHooksAllowed(false, '/opt/ai-desktop/ai-desktop')).toBe(true);
    expect(testHooksAllowed(true, '/opt/ai-desktop/ai-desktop')).toBe(false); // packaged, no test-copy marker
  });
});

describe('health', () => {
  it('reads the agent answer', async () => {
    agent.version = '0.1.0';
    expect(await client().health()).toEqual({ reachable: true, health: { ok: true, version: '0.1.0', mode: 'online', starting: false } });
  });

  it('retries 3 times, 300 ms apart, then says unreachable', async () => {
    await agent.close();
    const sleeps: number[] = [];
    const result = await client({ sleep: async (ms) => void sleeps.push(ms) }).health();
    expect(result).toEqual({ reachable: false, reason: 'refused' });
    expect(sleeps).toEqual([300, 300]);
    agent = await startFakeAgent({ port: 0 }); // so afterEach can close something
  });

  it('succeeds on a later try', async () => {
    let calls = 0;
    const result = await client({
      fetch: async (input, init) => {
        calls += 1;
        if (calls < 3) throw Object.assign(new TypeError('fetch failed'), { cause: { code: 'ECONNREFUSED' } });
        return fetch(input, init);
      },
    }).health();
    expect(result.reachable).toBe(true);
    expect(calls).toBe(3);
  });

  it('something else on the port is "invalid", not an agent', async () => {
    const result = await client({ fetch: async () => new Response('<html>', { status: 200 }) }).health({ tries: 1 });
    expect(result).toEqual({ reachable: false, reason: 'invalid' });
  });
});

describe('session', () => {
  it('posts the token from the file with the JSON content type and an empty object', async () => {
    const result = await client().createSession();
    expect(result).toEqual({ ok: true });
    expect(agent.sessionRequests).toEqual([{ authorization: `Bearer ${agent.token}`, accepted: true, origin: undefined }]);
  });

  it('401 is reported as unauthorized with the agent error code', async () => {
    agent.rejectLogin = true;
    expect(await client().createSession()).toEqual({ ok: false, kind: 'unauthorized', status: 401, code: 'LAI-SEC-001' });
  });

  it('a rotated token: recreateSession re-reads the file and succeeds', async () => {
    const c = client();
    expect((await c.createSession()).ok).toBe(true);
    agent.token = newFakeToken();
    await writeFile(tokenPath, agent.token);
    expect(await c.createSession()).toMatchObject({ ok: false, kind: 'unauthorized' }); // still the old token in memory
    expect(await c.recreateSession()).toEqual({ ok: true });
    expect(agent.sessionRequests.at(-1)).toMatchObject({ authorization: `Bearer ${agent.token}`, accepted: true });
  });

  it('an unusable token file is reported, not thrown', async () => {
    const result = await client({ tokenPath: join(dir, 'missing') }).createSession();
    expect(result).toMatchObject({ ok: false, kind: 'token', token: { reason: 'missing' } });
  });

  it('an agent that is gone is "unreachable"', async () => {
    await agent.close();
    expect(await client().createSession()).toEqual({ ok: false, kind: 'unreachable' });
    agent = await startFakeAgent({ port: 0 });
  });
});

describe('request()', () => {
  it('adds the bearer token and returns the response', async () => {
    const response = await client().request('/api/v1/status');
    expect(response.status).toBe(200);
  });

  it('refuses any destination that is not the agent API, before the token is attached', async () => {
    const seen: string[] = [];
    const c = client({ fetch: async (input) => (seen.push(String(input)), new Response('{}')) });
    for (const path of ['https://ai.marin.cr/api/v1/status', '//example.test/api/v1/status', '/other', '/api/v1/../x', 'api/v1/status', '/api/v10/x', 'http://127.0.0.1:9/api/v1/x']) {
      await expect(c.request(path), path).rejects.toBeInstanceOf(RangeError);
    }
    expect(seen).toEqual([]);
  });

  it('on a 401 it re-reads the token and repeats once if it changed', async () => {
    const c = client();
    expect((await c.request('/api/v1/status')).status).toBe(200);
    agent.token = newFakeToken();
    await writeFile(tokenPath, agent.token);
    expect((await c.request('/api/v1/status')).status).toBe(200);
  });

  it('on a 401 with an unchanged token it returns the 401 (no loop)', async () => {
    const c = client();
    expect((await c.request('/api/v1/status')).status).toBe(200);
    agent.unauthorizedAll = true;
    expect((await c.request('/api/v1/status')).status).toBe(401);
  });

  it('throws AgentTokenError when there is no usable token', async () => {
    await expect(client({ tokenPath: join(dir, 'missing') }).request('/api/v1/status')).rejects.toBeInstanceOf(AgentTokenError);
  });

  it('status() reads the snapshot', async () => {
    const result = await client().status();
    expect(result).toMatchObject({ ok: true, status: { node: { id: 'ai-n3' }, mode: { value: 'online' } } });
    agent.unauthorizedAll = true;
    expect(await client().status()).toMatchObject({ ok: false, kind: 'unauthorized' });
  });

  it('never logs the token', async () => {
    const c = client();
    await c.createSession();
    await c.status();
    agent.unauthorizedAll = true;
    await c.status();
    expect(JSON.stringify(lines)).not.toContain(agent.token);
  });
});

describe('SSE parser', () => {
  it('parses frames split across chunks, CRLF, comments and multi-line data', () => {
    const p = new SseParser();
    const frames = [
      ...p.push('id: 7\nevent: model.loaded\nda'),
      ...p.push('ta: {"a":1}\r\n\r\n: ping\n\nevent: x\ndata: l1\ndata: l2\n'),
      ...p.push('\n'),
    ];
    expect(frames).toEqual([
      { id: '7', event: 'model.loaded', data: '{"a":1}' },
      { event: 'x', data: 'l1\nl2' },
    ]);
  });

  it('turns the documented envelope and the bare resync/shutdown payloads into events', () => {
    expect(frameToEvent({ id: '1042', event: 'model.loaded', data: JSON.stringify({ id: 1042, type: 'model.loaded', at: 't', node: 'ai-n3', op_id: 'op_1', data: { model: 'm' } }) })).toEqual({
      id: 1042, type: 'model.loaded', at: 't', node: 'ai-n3', opId: 'op_1', data: { model: 'm' },
    });
    expect(frameToEvent({ event: 'resync', data: '{"reason":"gap"}' })).toEqual({ id: null, type: 'resync', data: { reason: 'gap' } });
    expect(frameToEvent({ event: 'shutdown', data: '{}' })).toEqual({ id: null, type: 'shutdown', data: {} });
    expect(frameToEvent({ event: 'model.loaded', data: 'not json' })).toBeNull();
  });
});

describe('events()', () => {
  const collect = (c: AgentClient) => {
    const events: AgentEvent[] = [];
    const states: ConnectionState[] = [];
    const resyncs: string[] = [];
    const sub = c.events(
      { onEvent: (e) => void events.push(e), onState: (s) => void states.push(s), onResync: (r) => void resyncs.push(r) },
      { backoff: { minMs: 1000, maxMs: 30_000 } },
    );
    return { events, states, resyncs, sub };
  };

  it('streams events with the bearer token and no cookie', async () => {
    const { events, states, sub } = collect(client());
    await until(() => agent.openEventStreams() === 1);
    const id = agent.emit('model.loaded', { model: 'qwen', load_ms: 5 });
    await until(() => events.length === 1);
    expect(events[0]).toMatchObject({ id, type: 'model.loaded', data: { model: 'qwen' } });
    expect(states).toContain('connected');
    expect(agent.eventConnections).toEqual([{ lastEventId: undefined, via: 'bearer' }]);
    sub.close();
    expect(sub.state).toBe('closed');
    await until(() => agent.openEventStreams() === 0);
  });

  it('reconnects with Last-Event-ID after a drop and asks for a /status re-read', async () => {
    const delays: number[] = [];
    const c = client({ sleep: async (ms) => (delays.push(ms), tick()) });
    const { events, resyncs, sub } = collect(c);
    await until(() => agent.openEventStreams() === 1);
    const first = agent.emit('mode.changed', { from: 'online', to: 'offline' });
    await until(() => events.length === 1);
    agent.dropEventStreams();
    const missed = agent.emit('model.unloaded', { model: 'm', reason: 'request' });
    await until(() => agent.eventConnections.length === 2);
    expect(agent.eventConnections[1]!.lastEventId).toBe(String(first));
    await until(() => events.length === 2);
    expect(events[1]).toMatchObject({ id: missed, type: 'model.unloaded' });
    expect(resyncs).toEqual(['reconnect']);
    expect(delays[0]).toBe(1000);
    sub.close();
  });

  it('a "gap" resync from the agent reaches the handler and does not move Last-Event-ID', async () => {
    const body = 'event: resync\ndata: {"reason":"gap"}\n\nid: 5\nevent: mode.changed\ndata: {"id":5,"type":"mode.changed","data":{"to":"offline"}}\n\n';
    const resyncs: string[] = [];
    const events: AgentEvent[] = [];
    let calls = 0;
    const c = client({
      fetch: async () => {
        calls += 1;
        return new Response(calls === 1 ? body : null, { status: calls === 1 ? 200 : 503, headers: { 'content-type': 'text/event-stream' } });
      },
    });
    const sub = c.events({ onEvent: (e) => void events.push(e), onResync: (r) => void resyncs.push(r) });
    await until(() => events.length === 2);
    expect(resyncs).toEqual(['gap']);
    expect(events[0]).toMatchObject({ id: null, type: 'resync' });
    expect(sub.lastEventId).toBe(5);
    sub.close();
  });

  it('a 200 that is not an event stream is not "connected" (something else on the port)', async () => {
    const states: ConnectionState[] = [];
    const c = client({ fetch: async () => new Response('<h1>hi</h1>', { status: 200, headers: { 'content-type': 'text/html' } }) });
    const sub = c.events({ onEvent: () => undefined, onState: (s) => void states.push(s) });
    await new Promise((r) => setTimeout(r, 30));
    sub.close();
    expect(states).not.toContain('connected');
  });

  it('marks shutdown, then reconnects with a growing, capped backoff', async () => {
    const delays: number[] = [];
    const { states, sub } = collect(client({ sleep: async (ms) => (delays.push(ms), tick()) }));
    await until(() => agent.openEventStreams() === 1);
    agent.shutdownEventStreams();
    await until(() => states.includes('shutdown'));
    await until(() => agent.eventConnections.length >= 2);
    sub.close();
    expect(delays[0]).toBe(1000);
  });

  it('backs off 1, 2, 4, ... up to 30 s while the agent is down, and starts over after a good connection', async () => {
    await agent.close();
    const delays: number[] = [];
    let reached = false;
    const c = client({
      sleep: async (ms) => {
        delays.push(ms);
        if (delays.length >= 8) reached = true;
        await new Promise((r) => setTimeout(r, 1));
      },
    });
    const { sub } = collect(c);
    await until(() => reached);
    sub.close();
    expect(delays.slice(0, 7)).toEqual([1000, 2000, 4000, 8000, 16_000, 30_000, 30_000]);
    agent = await startFakeAgent({ port: 0 });
  });

  it('a 401 re-reads the token and tells the handler (state unauthorized)', async () => {
    agent.unauthorizedAll = true;
    const { states, sub } = collect(client());
    await until(() => states.includes('unauthorized'));
    sub.close();
  });

  it('AgentFeed re-reads /status after a reconnect and tells its subscribers', async () => {
    const c = client();
    const feed = new AgentFeed(c);
    const seen: string[] = [];
    feed.subscribe((m) => void seen.push(m.kind === 'event' ? `event:${m.event.type}` : m.kind === 'connection' ? `connection:${m.state}` : 'status'));
    feed.start();
    await until(() => agent.openEventStreams() === 1);
    agent.dropEventStreams();
    await until(() => agent.eventConnections.length === 2);
    await until(() => seen.includes('status'));
    expect(feed.status?.node?.id).toBe('ai-n3');
    feed.stop();
  });
});
