// The alert engine over an in-memory store: raise, refresh, escalate, clear, notify exactly once, repeat, acknowledge,
// expected silence, retired nodes, external kinds and concurrent evaluations. The same flows against real SQL and the
// real unique index are `test/localai/alerts.db.spec.ts`.
import 'reflect-metadata';

import { ConflictException, NotFoundException } from '@nestjs/common';

import type { AlertChangedEvent, AlertNotice, AlertNotifier } from './alert-ports';
import { ALERT_CHANGED_CHANNEL } from './alert-ports';
import type { AlertSettings } from './alert-settings';
import type { EvaluationNode } from './alert-store';
import { DAY, HOUR, MIN, NOW, ago, node, settings, state } from './alert-fixtures';
import { FakeAlertStore } from './fake-alert.store';
import { AUDIT_ALERT_ACKNOWLEDGED, AlertsService } from './alerts.service';

function evalNode(patch: Partial<EvaluationNode> = {}): EvaluationNode {
  return { ...node(), rowId: 'row-1', displayName: 'Node One', ...patch };
}

function setup(options: { settings?: Partial<AlertSettings> } = {}) {
  const store = new FakeAlertStore();
  const clock = { current: NOW, now: () => clock.current };
  const raised: AlertNotice[] = [];
  const cleared: AlertNotice[] = [];
  const notifier: AlertNotifier = {
    raised: jest.fn(async (notice) => void raised.push(notice)),
    cleared: jest.fn(async (notice) => void cleared.push(notice)),
  };
  const events: { channel: string; payload: AlertChangedEvent }[] = [];
  const bus = { publish: jest.fn(async (channel: string, payload: AlertChangedEvent) => void events.push({ channel, payload })) };
  const audit = { record: jest.fn(async () => undefined) };
  const settingsReader = { read: jest.fn(async () => settings(options.settings)) };
  const service = new AlertsService(store, settingsReader as never, notifier, bus as never, audit as never, clock);
  return { store, clock, service, raised, cleared, events, notifier, audit, settingsReader };
}

const driveAge = (days: number, from: Date = NOW) => state({ ark: { roots: [{ id: 'internal', tiers: { t1: 'ok', t2: 'ok' } }], drive_last_synced_at: ago(days * DAY + HOUR, from).toISOString() } });
/** A node that reported at `at` and whose drive was last synced `days` before it: only the drive is wrong. */
const freshAt = (at: Date, days: number): EvaluationNode => evalNode({ lastState: driveAge(days, at), lastStateAt: at, lastContactAt: at });

describe('raising', () => {
  it('raises a new alert once, notifies once, publishes alert.changed and remembers when it told', async () => {
    const { store, service, raised, events, clock } = setup();
    store.nodes = [evalNode({ lastState: driveAge(41) })];
    expect(await service.evaluateAll()).toMatchObject({ nodes: 1, raised: 1, notified: 1 });
    expect(store.rows).toHaveLength(1);
    expect(store.rows[0]).toMatchObject({ kind: 'ark.drive_stale', severity: 'warning', state: 'active', notifiedAt: clock.current });
    expect(raised).toEqual([expect.objectContaining({ node: 'ai-n1', code: 'LAI-CLD-022', severity: 'warning', title: 'Ark drive not synced for 41 days' })]);
    expect(raised[0]!.fix).toContain('Plug the AI-ARK drive');
    expect(events).toEqual([{ channel: ALERT_CHANGED_CHANNEL, payload: { alertId: 'alert-1', nodeId: 'ai-n1', kind: 'ark.drive_stale', state: 'active' } }]);
  });

  it('a second evaluation refreshes the alert and does NOT notify again', async () => {
    const { store, service, raised, clock } = setup();
    store.nodes = [evalNode({ lastState: driveAge(41) })];
    await service.evaluateAll();
    clock.current = new Date(NOW.getTime() + 5 * MIN);
    store.nodes = [evalNode({ lastState: driveAge(41) })];
    expect(await service.evaluateAll()).toMatchObject({ raised: 0, notified: 0 });
    expect(store.rows).toHaveLength(1);
    expect(store.rows[0]!.lastSeenAt).toEqual(clock.current);
    expect(raised).toHaveLength(1);
  });

  it('a node-reported critical alert gets its own row per code', async () => {
    const { store, service } = setup();
    store.nodes = [evalNode({ lastState: state({ alerts: [{ code: 'LAI-HW-020', severity: 'critical', title: 'a' }, { code: 'LAI-HW-021', severity: 'critical', title: 'b' }] }) })];
    await service.evaluateAll();
    expect(store.rows.map((row) => row.kind).sort()).toEqual(['node.alerts.LAI-HW-020', 'node.alerts.LAI-HW-021']);
  });

  it('an unknown code gets the generic fix, never an empty one', async () => {
    const { store, service, raised } = setup();
    store.nodes = [evalNode({ lastState: state({ alerts: [{ code: 'LAI-HW-020', severity: 'critical', title: 'a' }] }) })];
    await service.evaluateAll();
    expect(raised[0]!.fix.length).toBeGreaterThan(10);
  });
});

describe('escalation', () => {
  it('moves the same row from warning to critical and announces it again, forgetting the acknowledgement', async () => {
    const { store, service, raised, clock } = setup();
    store.nodes = [evalNode({ lastState: driveAge(41) })];
    await service.evaluateAll();
    await service.acknowledge('alert-1', 'user-1');
    expect(store.rows[0]!.acknowledgedAt).not.toBeNull();

    clock.current = new Date(NOW.getTime() + 55 * DAY);
    store.nodes = [freshAt(clock.current, 96)];
    expect(await service.evaluateAll()).toMatchObject({ raised: 0, escalated: 1, notified: 1 });
    expect(store.rows).toHaveLength(1);
    expect(store.rows[0]).toMatchObject({ id: 'alert-1', severity: 'critical', acknowledgedAt: null, title: 'Ark drive not synced for 96 days' });
    expect(raised.map((notice) => notice.severity)).toEqual(['warning', 'critical']);
  });

  it('a de-escalation (the threshold was raised) changes the severity quietly', async () => {
    const { store, service, raised } = setup();
    store.nodes = [evalNode({ lastState: driveAge(95) })];
    await service.evaluateAll();
    expect(store.rows[0]!.severity).toBe('critical');
    store.nodes = [evalNode({ lastState: driveAge(95) })];
    (service as unknown as { settings: { read: () => Promise<unknown> } }).settings.read = async () => settings({ driveCriticalDays: 120 });
    expect(await service.evaluateAll()).toMatchObject({ escalated: 0, notified: 0 });
    expect(store.rows[0]!.severity).toBe('warning');
    expect(raised).toHaveLength(1);
  });
});

describe('clearing', () => {
  it('clears an alert whose condition is gone, tells the people who were told, and publishes it', async () => {
    const { store, service, cleared, events } = setup();
    store.nodes = [evalNode({ lastState: driveAge(41) })];
    await service.evaluateAll();
    store.nodes = [evalNode({ lastState: driveAge(1) })];
    expect(await service.evaluateAll()).toMatchObject({ cleared: 1 });
    expect(store.rows[0]).toMatchObject({ state: 'cleared', clearedAt: NOW });
    expect(cleared).toEqual([expect.objectContaining({ node: 'ai-n1', title: 'Ark drive not synced for 41 days' })]);
    expect(events.at(-1)!.payload).toMatchObject({ state: 'cleared', kind: 'ark.drive_stale' });
  });

  it('a cleared alert can be raised again as a NEW row', async () => {
    const { store, service } = setup();
    store.nodes = [evalNode({ lastState: driveAge(41) })];
    await service.evaluateAll();
    store.nodes = [evalNode({ lastState: driveAge(1) })];
    await service.evaluateAll();
    store.nodes = [evalNode({ lastState: driveAge(45) })];
    await service.evaluateAll();
    expect(store.rows.map((row) => [row.id, row.state])).toEqual([['alert-1', 'cleared'], ['alert-2', 'active']]);
  });

  it('does not announce the resolution of an alert nobody was ever told about', async () => {
    const { store, service, cleared } = setup();
    store.nodes = [evalNode({ lastState: driveAge(41) })];
    await service.evaluateAll();
    store.rows[0]!.notifiedAt = null;
    store.nodes = [evalNode({ lastState: driveAge(1) })];
    await service.evaluateAll();
    expect(cleared).toHaveLength(0);
  });

  it('clears the active alerts of a node that was retired', async () => {
    const { store, service } = setup();
    store.nodes = [evalNode({ lastState: driveAge(41) })];
    await service.evaluateAll();
    store.nodes = [];
    store.retiredNodeRowIds.add('row-1');
    expect(await service.evaluateAll()).toMatchObject({ nodes: 0, cleared: 1 });
    expect(store.rows[0]!.state).toBe('cleared');
  });
});

describe('the stale-node story, end to end over the fake store', () => {
  it('goes stale -> node.stale raised and announced once -> recovers -> cleared and resolved', async () => {
    const { store, service, raised, cleared, clock } = setup();
    const silent = (ms: number) => evalNode({ lastStateAt: ago(ms, clock.current), lastContactAt: ago(ms, clock.current), eab: 'stale' });
    store.nodes = [silent(4 * MIN)];
    await service.evaluateAll();
    expect(store.rows).toHaveLength(0);

    store.nodes = [silent(11 * MIN)];
    await service.evaluateAll();
    expect(store.rows.map((row) => [row.kind, row.state])).toEqual([['node.stale', 'active']]);
    expect(raised).toHaveLength(1);

    clock.current = new Date(NOW.getTime() + 5 * MIN);
    store.nodes = [silent(16 * MIN)];
    await service.evaluateAll();
    expect(raised).toHaveLength(1);

    store.nodes = [evalNode({ lastStateAt: clock.current, lastContactAt: clock.current, eab: 'healthy' })];
    await service.evaluateAll();
    expect(store.rows[0]!.state).toBe('cleared');
    expect(cleared).toHaveLength(1);
  });
});

describe('expected silence', () => {
  it('a node that goes to offline mode stops being stale at once: the alert is cleared, nothing new is raised', async () => {
    const { store, service } = setup();
    const quiet = { lastStateAt: ago(3 * HOUR), lastContactAt: ago(3 * HOUR), eab: 'offline' as const };
    store.nodes = [evalNode({ ...quiet })];
    await service.evaluateAll();
    expect(store.rows.map((row) => row.kind).sort()).toEqual(['node.stale']);
    store.nodes = [evalNode({ ...quiet, mode: 'offline', lastState: state({ mode: 'offline' }) })];
    await service.evaluateNode('ai-n1');
    expect(store.rows.map((row) => [row.kind, row.state])).toEqual([['node.stale', 'cleared']]);
  });

  it('a pending node raises nothing', async () => {
    const { store, service } = setup();
    store.nodes = [evalNode({ phase: 'pending', mode: null, lastState: null, lastStateAt: null, lastContactAt: null, keyReportedAt: null })];
    await service.evaluateAll();
    expect(store.rows).toHaveLength(0);
  });
});

describe('repeat and acknowledge', () => {
  it('an unacknowledged alert is announced again after alerts.repeatHours, not before', async () => {
    const { store, service, raised, clock } = setup({ settings: { repeatHours: 24 } });
    store.nodes = [evalNode({ lastState: driveAge(41) })];
    await service.evaluateAll();
    clock.current = new Date(NOW.getTime() + 24 * HOUR - MIN);
    store.nodes = [freshAt(clock.current, 41)];
    await service.evaluateAll();
    expect(raised).toHaveLength(1);
    clock.current = new Date(NOW.getTime() + 24 * HOUR + MIN);
    store.nodes = [freshAt(clock.current, 41)];
    expect(await service.evaluateAll()).toMatchObject({ notified: 1 });
    expect(raised).toHaveLength(2);
  });

  it('acknowledging stops the repeats and does not clear the alert', async () => {
    const { store, service, raised, clock, audit } = setup();
    store.nodes = [evalNode({ lastState: driveAge(41) })];
    await service.evaluateAll();
    const acked = await service.acknowledge('alert-1', 'user-1');
    expect(acked).toMatchObject({ state: 'active', acknowledgedAt: NOW });
    expect(audit.record).toHaveBeenCalledWith(expect.objectContaining({ action: AUDIT_ALERT_ACKNOWLEDGED, actorUserId: 'user-1', targetId: 'alert-1', meta: { kind: 'ark.drive_stale', nodeId: 'ai-n1' } }));
    for (const days of [2, 5, 30]) {
      clock.current = new Date(NOW.getTime() + days * DAY);
      store.nodes = [freshAt(clock.current, 41)];
      await service.evaluateAll();
    }
    expect(raised).toHaveLength(1);
    expect(store.rows[0]!.state).toBe('active');
  });

  it('acknowledging twice keeps the first acknowledgement and audits once', async () => {
    const { store, service, audit, clock } = setup();
    store.nodes = [evalNode({ lastState: driveAge(41) })];
    await service.evaluateAll();
    await service.acknowledge('alert-1', 'user-1');
    clock.current = new Date(NOW.getTime() + HOUR);
    const again = await service.acknowledge('alert-1', 'user-2');
    expect(again.acknowledgedAt).toEqual(NOW);
    expect(audit.record).toHaveBeenCalledTimes(1);
  });

  it('an unknown alert is a 404 and a cleared one a 409', async () => {
    const { store, service } = setup();
    await expect(service.acknowledge('nope', 'user-1')).rejects.toBeInstanceOf(NotFoundException);
    store.nodes = [evalNode({ lastState: driveAge(41) })];
    await service.evaluateAll();
    store.nodes = [evalNode({ lastState: driveAge(1) })];
    await service.evaluateAll();
    await expect(service.acknowledge('alert-1', 'user-1')).rejects.toBeInstanceOf(ConflictException);
  });

  it('a failing audit sink does not fail the acknowledgement', async () => {
    const { store, service, audit } = setup();
    audit.record.mockRejectedValueOnce(new Error('sink down'));
    store.nodes = [evalNode({ lastState: driveAge(41) })];
    await service.evaluateAll();
    await expect(service.acknowledge('alert-1', 'user-1')).resolves.toMatchObject({ acknowledgedAt: NOW });
  });
});

describe('concurrent evaluations', () => {
  it('two evaluations at once raise and announce exactly one alert', async () => {
    const { store, service, raised } = setup();
    store.nodes = [evalNode({ lastState: driveAge(41) })];
    await Promise.all([service.evaluateAll(), service.evaluateAll(), service.evaluateNode('ai-n1')]);
    expect(store.rows).toHaveLength(1);
    expect(raised).toHaveLength(1);
  });

  it('two evaluations at once clear and resolve exactly once', async () => {
    const { store, service, cleared } = setup();
    store.nodes = [evalNode({ lastState: driveAge(41) })];
    await service.evaluateAll();
    store.nodes = [evalNode({ lastState: driveAge(1) })];
    await Promise.all([service.evaluateAll(), service.evaluateAll()]);
    expect(cleared).toHaveLength(1);
  });
});

describe('notifier failures', () => {
  it('a notifier that throws does not stop the evaluation of the other nodes', async () => {
    const { store, service, notifier } = setup();
    (notifier.raised as jest.Mock).mockRejectedValueOnce(new Error('no mail'));
    store.nodes = [evalNode({ lastState: driveAge(41) }), evalNode({ rowId: 'row-2', nodeId: 'ai-n2', lastState: driveAge(41) })];
    await expect(service.evaluateAll()).resolves.toMatchObject({ raised: 2 });
    expect(store.rows).toHaveLength(2);
  });

  it('a bus that throws does not stop anything either', async () => {
    const { store, service } = setup();
    (service as unknown as { bus: { publish: () => Promise<void> } }).bus.publish = async () => {
      throw new Error('bus down');
    };
    store.nodes = [evalNode({ lastState: driveAge(41) })];
    await expect(service.evaluateAll()).resolves.toMatchObject({ raised: 1 });
  });
});

describe('external alerts (endpoint.down)', () => {
  const probe = { code: 'LAI-CLD-027', severity: 'warning' as const, title: 'ai-n1.example.test is not answering' };

  it('raiseExternal creates the alert and notifies; a second call refreshes it', async () => {
    const { store, service, raised } = setup();
    store.nodes = [evalNode()];
    expect(await service.raiseExternal('ai-n1', 'endpoint.down', probe)).toBe(true);
    expect(await service.raiseExternal('ai-n1', 'endpoint.down', probe)).toBe(false);
    expect(store.rows).toHaveLength(1);
    expect(raised).toHaveLength(1);
  });

  it('the evaluator never clears an external alert, even though its rules never produce it', async () => {
    const { store, service } = setup();
    store.nodes = [evalNode()];
    await service.raiseExternal('ai-n1', 'endpoint.down', probe);
    await service.evaluateAll();
    await service.evaluateNode('ai-n1');
    expect(store.rows[0]).toMatchObject({ kind: 'endpoint.down', state: 'active' });
  });

  it('clearExternal clears it once and tells the operators', async () => {
    const { store, service, cleared } = setup();
    store.nodes = [evalNode()];
    await service.raiseExternal('ai-n1', 'endpoint.down', probe);
    expect(await service.clearExternal('ai-n1', 'endpoint.down')).toBe(true);
    expect(await service.clearExternal('ai-n1', 'endpoint.down')).toBe(false);
    expect(cleared).toHaveLength(1);
  });

  it('refuses a kind the gateway does not own, and an unknown node is a no-op', async () => {
    const { store, service } = setup();
    store.nodes = [evalNode()];
    await expect(service.raiseExternal('ai-n1', 'node.stale', probe)).rejects.toThrow(/not an externally owned/);
    await expect(service.clearExternal('ai-n1', 'node.stale')).rejects.toThrow(/not an externally owned/);
    expect(await service.raiseExternal('ai-n99', 'endpoint.down', probe)).toBe(false);
  });
});

describe('list', () => {
  it('orders active alerts most urgent first, then newest', async () => {
    const { store, service, clock } = setup();
    store.nodes = [evalNode({ lastState: driveAge(41) })];
    await service.evaluateAll();
    clock.current = new Date(NOW.getTime() + HOUR);
    store.nodes = [evalNode({ lastState: state({ ...driveAge(41, clock.current), alerts: [{ code: 'LAI-HW-020', severity: 'critical', title: 'c' }] }), lastStateAt: clock.current, lastContactAt: clock.current })];
    await service.evaluateAll();
    const listed = await service.list({ state: 'active', limit: 100 });
    expect(listed.map((row) => row.severity)).toEqual(['critical', 'warning']);
  });
});
