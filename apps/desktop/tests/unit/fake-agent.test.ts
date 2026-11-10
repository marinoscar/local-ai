import { afterEach, describe, expect, it } from 'vitest';
import { type FakeAgent, startFakeAgent } from '../fixtures/fake-agent';

let agent: FakeAgent | undefined;
afterEach(async () => {
  await agent?.close();
  agent = undefined;
});

describe('fake agent', () => {
  it('answers /api/v1/health like the real agent', async () => {
    agent = await startFakeAgent({ port: 0, mode: 'survival', version: '9.9.9' });
    const res = await fetch(`${agent.url}/api/v1/health`);
    expect(res.status).toBe(200);
    expect(await res.json()).toEqual({ ok: true, version: '9.9.9', mode: 'survival', starting: false });
    expect(agent.requests).toEqual([{ method: 'GET', path: '/api/v1/health' }]);
  });

  it('answers 404 for anything else and listens on loopback only', async () => {
    agent = await startFakeAgent({ port: 0 });
    expect((await fetch(`${agent.url}/api/v1/nope`)).status).toBe(404);
    expect(agent.url.startsWith('http://127.0.0.1:')).toBe(true);
  });

  it('serves the egress probe page and records what it reports', async () => {
    agent = await startFakeAgent({ port: 0 });
    const page = await fetch(`${agent.url}/egress-probe`);
    expect(page.headers.get('content-type')).toContain('text/html');
    expect(page.headers.get('content-security-policy')).toBeNull();
    const html = await page.text();
    expect(html).toContain('new WebSocket(');
    expect(html).toContain('new Image()');
    const posted = await fetch(`${agent.url}/egress-result`, { method: 'POST', body: JSON.stringify({ fetch: 'failed', img: 'failed', ws: 'failed' }) });
    expect(posted.status).toBe(204);
    expect(agent.probeResults).toEqual([{ fetch: 'failed', img: 'failed', ws: 'failed' }]);
  });

  it('serves the ensure picture, records each call, and wants the PIN grant only for a confirmation', async () => {
    agent = await startFakeAgent({ port: 0 });
    const headers = { authorization: `Bearer ${agent.token}`, 'content-type': 'application/json' };
    expect((await fetch(`${agent.url}/api/v1/ensure`)).status).toBe(401);
    const got = await fetch(`${agent.url}/api/v1/ensure`, { headers });
    expect(((await got.json()) as { state: string }).state).toBe('needs_you');
    const started = await fetch(`${agent.url}/api/v1/ensure`, { method: 'POST', headers, body: '{}' });
    expect(started.status).toBe(202);
    const refused = await fetch(`${agent.url}/api/v1/ensure`, { method: 'POST', headers, body: JSON.stringify({ confirm: ['whisper.build'] }) });
    expect(refused.status).toBe(403);
    expect(agent.ensureRequests.map((r) => [r.method, r.accepted])).toEqual([['GET', false], ['GET', true], ['POST', true], ['POST', false]]);
  });
});
