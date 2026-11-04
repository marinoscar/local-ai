import { Logger } from '@nestjs/common';

import type { EndpointAlertsPort, EndpointDownDetails } from './endpoint-alerts.port';
import { EndpointProbe, fetchHealthClient, type HealthClient, type ProbeResult } from './probe';
import { FakeBus, FakeClock, model, node } from './routing.fixtures';
import type { RoutingSource } from './routing-source';
import { RoutingTable } from './routing-table';

beforeAll(() => {
  for (const level of ['log', 'debug', 'warn', 'error'] as const) jest.spyOn(Logger.prototype, level).mockImplementation(() => undefined);
});
afterAll(() => jest.restoreAllMocks());

class FakeHealth implements HealthClient {
  readonly urls: string[] = [];
  results = new Map<string, ProbeResult>();
  fallback: ProbeResult = { ok: true };
  gate: Promise<void> | null = null;
  async check(url: string, timeoutMs: number): Promise<ProbeResult> {
    this.urls.push(`${url}|${timeoutMs}`);
    if (this.gate) await this.gate;
    return this.results.get(url) ?? this.fallback;
  }
}

class FakeAlerts implements EndpointAlertsPort {
  raised: Array<{ nodeId: string; details: EndpointDownDetails }> = [];
  cleared: string[] = [];
  failRaise = false;
  failClear = false;
  async raiseDown(nodeId: string, details: EndpointDownDetails): Promise<void> {
    if (this.failRaise) throw new Error('alerts down');
    this.raised.push({ nodeId, details });
  }
  async clearDown(nodeId: string): Promise<void> {
    if (this.failClear) throw new Error('alerts down');
    this.cleared.push(nodeId);
  }
}

async function setup() {
  const rows = [node('ai-n1', [model('model-a')]), node('ai-n2', [model('model-a')])];
  const source: RoutingSource = { load: async () => ({ rows, settings: { queueFactor: 4 } }) };
  const table = new RoutingTable(source, new FakeBus() as never, new FakeClock(), { timers: false, rebuildIntervalMs: 10_000 });
  await table.rebuild();
  const health = new FakeHealth();
  const alerts = new FakeAlerts();
  const probe = new EndpointProbe(table, health, alerts, { timers: false, rebuildIntervalMs: 10_000 });
  return { rows, table, health, alerts, probe };
}

const fail: ProbeResult = { ok: false, reason: 'timeout' };
const URL1 = 'https://ai-n1.example.test/health';
const URL2 = 'https://ai-n2.example.test/health';

describe('EndpointProbe: what it calls', () => {
  it('checks https://<publicHost>/health of every routable node with a 3 s timeout and no key', async () => {
    const { probe, health } = await setup();
    await probe.runOnce();
    expect([...health.urls].sort()).toEqual([`${URL1}|3000`, `${URL2}|3000`]);
  });

  it('checks nothing when no node is routable', async () => {
    const { probe, health, rows, table } = await setup();
    rows.length = 0;
    await table.rebuild();
    await probe.runOnce();
    expect(health.urls).toEqual([]);
  });

  it('is one run at a time: a tick during a run is a no-op', async () => {
    const { probe, health } = await setup();
    let release!: () => void;
    health.gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    const first = probe.runOnce();
    const second = probe.runOnce();
    expect(second).toBe(first);
    release();
    await first;
    expect(health.urls).toHaveLength(2);
  });
});

describe('EndpointProbe: the state machine', () => {
  it('two failures change nothing', async () => {
    const { probe, health, table, alerts } = await setup();
    health.results.set(URL1, fail);
    await probe.runOnce();
    await probe.runOnce();
    expect(table.isSuspect('ai-n1')).toBe(false);
    expect(alerts.raised).toEqual([]);
  });

  it('three consecutive failures: the node is suspect and endpoint.down is raised once', async () => {
    const { probe, health, table, alerts } = await setup();
    health.results.set(URL1, { ok: false, reason: 'status_503' });
    for (let i = 0; i < 3; i += 1) await probe.runOnce();
    expect(table.isSuspect('ai-n1')).toBe(true);
    expect(table.isSuspect('ai-n2')).toBe(false);
    expect(table.select('model-a')).toMatchObject({ kind: 'candidates' });
    expect(alerts.raised).toEqual([{ nodeId: 'ai-n1', details: { publicHost: 'ai-n1.example.test', consecutiveFailures: 3, reason: 'status_503' } }]);
    for (let i = 0; i < 4; i += 1) await probe.runOnce();
    expect(alerts.raised).toHaveLength(1);
    expect(table.isSuspect('ai-n1')).toBe(true);
  });

  it('a success in between resets the count', async () => {
    const { probe, health, table, alerts } = await setup();
    health.results.set(URL1, fail);
    await probe.runOnce();
    await probe.runOnce();
    health.results.set(URL1, { ok: true });
    await probe.runOnce();
    health.results.set(URL1, fail);
    await probe.runOnce();
    await probe.runOnce();
    expect(table.isSuspect('ai-n1')).toBe(false);
    expect(alerts.raised).toEqual([]);
  });

  it('the next success lifts the suspicion and clears the alert', async () => {
    const { probe, health, table, alerts } = await setup();
    health.results.set(URL1, fail);
    for (let i = 0; i < 3; i += 1) await probe.runOnce();
    alerts.cleared.length = 0;
    health.results.set(URL1, { ok: true });
    await probe.runOnce();
    expect(table.isSuspect('ai-n1')).toBe(false);
    expect(alerts.cleared).toEqual(['ai-n1']);
    await probe.runOnce();
    expect(alerts.cleared).toEqual(['ai-n1']);
  });

  it('raises again after a recovery and three new failures', async () => {
    const { probe, health, alerts } = await setup();
    health.results.set(URL1, fail);
    for (let i = 0; i < 3; i += 1) await probe.runOnce();
    health.results.set(URL1, { ok: true });
    await probe.runOnce();
    health.results.set(URL1, fail);
    for (let i = 0; i < 3; i += 1) await probe.runOnce();
    expect(alerts.raised).toHaveLength(2);
  });

  it('looks for an alert left by an earlier process on the first success, once', async () => {
    const { probe, alerts } = await setup();
    await probe.runOnce();
    expect(alerts.cleared.sort()).toEqual(['ai-n1', 'ai-n2']);
    await probe.runOnce();
    expect(alerts.cleared).toHaveLength(2);
  });

  it('retries a failed raise on the next failed check', async () => {
    const { probe, health, alerts, table } = await setup();
    health.results.set(URL1, fail);
    alerts.failRaise = true;
    for (let i = 0; i < 3; i += 1) await probe.runOnce();
    expect(table.isSuspect('ai-n1')).toBe(true);
    expect(alerts.raised).toEqual([]);
    alerts.failRaise = false;
    await probe.runOnce();
    expect(alerts.raised).toHaveLength(1);
  });

  it('retries a failed clear on the next success', async () => {
    const { probe, health, alerts, table } = await setup();
    health.results.set(URL1, fail);
    for (let i = 0; i < 3; i += 1) await probe.runOnce();
    alerts.cleared.length = 0;
    alerts.failClear = true;
    health.results.set(URL1, { ok: true });
    await probe.runOnce();
    expect(table.isSuspect('ai-n1')).toBe(false);
    expect(alerts.cleared).toEqual([]);
    alerts.failClear = false;
    await probe.runOnce();
    expect(alerts.cleared).toContain('ai-n1');
  });

  it('forgets a node that left the table: the suspicion goes and the alert is cleared', async () => {
    const { probe, health, alerts, table, rows } = await setup();
    health.results.set(URL1, fail);
    for (let i = 0; i < 3; i += 1) await probe.runOnce();
    alerts.cleared.length = 0;
    rows[0] = node('ai-n1', [model('model-a')], { mode: 'offline' });
    await table.rebuild();
    await probe.runOnce();
    expect(table.isSuspect('ai-n1')).toBe(false);
    expect(alerts.cleared).toEqual(['ai-n1']);
  });

  it('never publishes an error message: the alert reason is one of a fixed set', async () => {
    const { probe, health, alerts } = await setup();
    health.results.set(URL1, { ok: false, reason: 'connect' });
    for (let i = 0; i < 3; i += 1) await probe.runOnce();
    expect(alerts.raised[0]!.details.reason).toMatch(/^(timeout|connect|status_\d{3})$/);
  });
});

describe('EndpointProbe: the timer', () => {
  beforeEach(() => jest.useFakeTimers());
  afterEach(() => jest.useRealTimers());

  it('runs every 30 s once bootstrapped and stops on destroy', async () => {
    const { rows, table, health } = await setup();
    const probe = new EndpointProbe(table, health, new FakeAlerts(), { timers: true, rebuildIntervalMs: 10_000 });
    expect(rows).toHaveLength(2);
    probe.onApplicationBootstrap();
    await jest.advanceTimersByTimeAsync(29_999);
    expect(health.urls).toHaveLength(0);
    await jest.advanceTimersByTimeAsync(1);
    expect(health.urls).toHaveLength(2);
    await jest.advanceTimersByTimeAsync(30_000);
    expect(health.urls).toHaveLength(4);
    probe.onModuleDestroy();
    await jest.advanceTimersByTimeAsync(90_000);
    expect(health.urls).toHaveLength(4);
  });

  it('starts no timer when timers are off', async () => {
    const { probe, health } = await setup();
    probe.onApplicationBootstrap();
    await jest.advanceTimersByTimeAsync(120_000);
    expect(health.urls).toHaveLength(0);
  });
});

describe('fetchHealthClient', () => {
  const realFetch = global.fetch;
  afterEach(() => {
    global.fetch = realFetch;
  });
  const respond = (status: number): void => {
    global.fetch = jest.fn(async () => new Response(null, { status })) as never;
  };

  it('treats 2xx as healthy', async () => {
    respond(200);
    await expect(fetchHealthClient.check('https://ai-n1.example.test/health', 3000)).resolves.toEqual({ ok: true });
  });
  it('treats 5xx, 4xx and a redirect as a failure with the status as the reason', async () => {
    for (const status of [503, 404, 302]) {
      respond(status);
      await expect(fetchHealthClient.check('https://ai-n1.example.test/health', 3000)).resolves.toEqual({ ok: false, reason: `status_${status}` });
    }
  });
  it('maps a timeout and any other error to fixed reasons, never a message', async () => {
    global.fetch = jest.fn(async () => {
      throw Object.assign(new Error('secret detail'), { name: 'TimeoutError' });
    }) as never;
    await expect(fetchHealthClient.check('https://x.example.test/health', 3000)).resolves.toEqual({ ok: false, reason: 'timeout' });
    global.fetch = jest.fn(async () => {
      throw new TypeError('fetch failed');
    }) as never;
    await expect(fetchHealthClient.check('https://x.example.test/health', 3000)).resolves.toEqual({ ok: false, reason: 'connect' });
  });
  it('sends no Authorization header and a 3 s abort signal', async () => {
    respond(200);
    await fetchHealthClient.check('https://ai-n1.example.test/health', 3000);
    const init = (global.fetch as jest.Mock).mock.calls[0]![1] as RequestInit;
    expect(JSON.stringify(init.headers ?? {}).toLowerCase()).not.toContain('authorization');
    expect(init.signal).toBeDefined();
    expect(init.redirect).toBe('manual');
  });
});
