// The alert engine against real PostgreSQL (docs/specs/cloud-app.md sections 4.9 and 9): the whole app on a migrated
// database. What only real SQL proves: `INSERT ... ON CONFLICT DO NOTHING` against the raw-SQL partial unique index
// `localai_alerts_one_active` (one active alert per (node, kind) under concurrent evaluations), the conditional writes
// that decide who announces a change, the notification rows the dispatcher writes for `lab:operate` holders, and the
// whole epic story: a node goes stale -> an alert row and a notification -> the node recovers -> the alert clears.
//
// Ingestion goes through the real `StateService` (the strict schema, the scrubber, the row lock). The evaluation clock is
// the real one plus an offset a test sets, so "41 days ago" is a number in the snapshot and "the node went quiet" is a
// row moved into the past with SQL (the same technique as the command plane's db spec).
import type { NestFastifyApplication } from '@nestjs/platform-fastify';
import { NotificationsService } from '@marinoscar/platform-api/notifications';

import { AlertsEvaluateHandler } from '../../src/localai/alerts/alerts-evaluate.job';
import { PrismaAlertStore } from '../../src/localai/alerts/prisma-alert.store';
import { AlertsService } from '../../src/localai/alerts/alerts.service';
import type { LocalaiStateSnapshotV1 } from '../../src/localai/contracts/state.schema';
import { StateService } from '../../src/localai/node-link/state.service';
import { createApp } from '../../src/main';
import { PrismaService } from '../../src/prisma/prisma.service';
import { ai3 } from './state-fixtures';

// Lab ids must look like ai-n<N>; a high range no real lab uses, cleaned up before and after.
const IDS = Array.from({ length: 8 }, (_, index) => `ai-n${9300 + index}`);
const run = `${Date.now()}`;
const DAY = 86_400_000;
const MIN = 60_000;

let app: NestFastifyApplication;
let prisma: PrismaService;
let service: AlertsService;
let store: PrismaAlertStore;
let state: StateService;
let notifications: NotificationsService;
let ownerId: string;
let operatorId: string;
let operatorToken: string;
let viewerToken: string;
let offset = 0;
let seq = 100;

const ago = (ms: number) => new Date(Date.now() - ms);
const ids = (...slots: number[]) => slots.map((slot) => IDS[slot]!);

async function login(role: 'contributor' | 'viewer', label: string): Promise<{ token: string; id: string }> {
  const email = `alerts-${label}-${run}@example.test`;
  const response = await app.inject({ method: 'POST', url: '/api/auth/test/login', payload: { email, role } });
  expect(response.statusCode).toBeLessThan(400);
  const token = new URL(String(response.headers.location)).searchParams.get('token') ?? '';
  expect(token).not.toBe('');
  return { token, id: (await prisma.user.findFirstOrThrow({ where: { email } })).id };
}

async function cleanup(): Promise<void> {
  await prisma.localaiAlert.deleteMany({ where: { node: { nodeId: { in: IDS } } } });
  await prisma.job.deleteMany({ where: { type: 'localai.alerts.evaluate', subjectId: { in: IDS } } });
  await prisma.auditEvent.deleteMany({ where: { targetId: { in: IDS }, action: { startsWith: 'localai.node.' } } });
  await prisma.localaiNode.deleteMany({ where: { nodeId: { in: IDS } } });
  await prisma.workerNode.deleteMany({ where: { name: { in: IDS } } });
}

/** An enrolled node with a live worker row and a reported key: nothing wrong with it until a test makes it so. */
async function makeNode(slot: number) {
  const nodeId = IDS[slot]!;
  const worker = await prisma.workerNode.create({
    data: { name: nodeId, hostname: 'host-alpha', platform: 'linux/amd64', cliVersion: 'aiagent/0.4.0', eligibleTypes: [], concurrency: 1, status: 'online', lastHeartbeatAt: new Date(), createdById: ownerId },
  });
  // The worker row is 30 days old, so a missing key would already be due.
  await prisma.workerNode.update({ where: { id: worker.id }, data: { registeredAt: ago(30 * DAY) } });
  await prisma.localaiNode.create({
    data: { nodeId, displayName: `Node ${slot}`, publicHost: `${nodeId}.example.test`, servePattern: 'B', phase: 'enrolled', workerNodeId: worker.id, keyReportedAt: new Date(), createdById: ownerId },
  });
  return { nodeId, workerId: worker.id };
}

/** Ingests a healthy snapshot of the ai-n3 fixture for `nodeId`; `patch` makes it say what a test is about. */
async function ingest(nodeId: string, patch: (snapshot: LocalaiStateSnapshotV1) => void = () => undefined) {
  const snapshot = ai3();
  snapshot.node_id = nodeId;
  seq += 1;
  snapshot.boot_id = '5f1c2d3e-4a5b-4c6d-8e7f-0a1b2c3d4e5f';
  snapshot.seq = seq;
  snapshot.reported_at = new Date().toISOString();
  snapshot.alerts = [];
  snapshot.agent_version = '0.4.1';
  if (snapshot.drill) Object.assign(snapshot.drill, { result: 'pass', failed_checks: [], failed_check_ids: [] });
  if (snapshot.sync.last_run) snapshot.sync.last_run.result = 'ok';
  snapshot.ark.drive_last_synced_at = ago(DAY).toISOString();
  patch(snapshot);
  const row = await prisma.localaiNode.findUniqueOrThrow({ where: { nodeId } });
  await state.ingest(row, ownerId, snapshot);
}

/** Moves a node's last snapshot and heartbeat into the past, as if it had gone quiet that long ago. */
async function goQuiet(nodeId: string, ms: number) {
  const row = await prisma.localaiNode.update({ where: { nodeId }, data: { lastStateAt: ago(ms) } });
  await prisma.workerNode.update({ where: { id: row.workerNodeId! }, data: { lastHeartbeatAt: ago(ms) } });
}

const activeOf = (nodeId: string) => prisma.localaiAlert.findMany({ where: { state: 'active', node: { nodeId } }, orderBy: { kind: 'asc' } });
const allOf = (nodeId: string) => prisma.localaiAlert.findMany({ where: { node: { nodeId } }, orderBy: { firstRaisedAt: 'asc' } });
const evaluate = async () => {
  const summary = await service.evaluateAll();
  await notifications.flush();
  return summary;
};
/** What the operator was told about a node (the bell rows the dispatcher wrote), oldest first. */
const bellOf = (nodeId: string, eventKey: string) =>
  prisma.notification.findMany({ where: { userId: operatorId, eventKey, title: { startsWith: `${nodeId}:` } }, orderBy: { createdAt: 'asc' } });
const mailsOf = (eventKey: string) => prisma.notificationDelivery.count({ where: { userId: operatorId, eventKey, channel: 'email' } });

beforeAll(async () => {
  app = await createApp();
  await app.init();
  await app.getHttpAdapter().getInstance().ready();
  prisma = app.get(PrismaService);
  service = app.get(AlertsService);
  store = app.get(PrismaAlertStore);
  state = app.get(StateService);
  notifications = app.get(NotificationsService);
  // The evaluation clock: real time plus the test's offset.
  (service as unknown as { clock: { now: () => Date } }).clock = { now: () => new Date(Date.now() + offset) };
  const owner = await login('contributor', 'owner');
  ownerId = owner.id;
  operatorId = owner.id;
  operatorToken = owner.token;
  viewerToken = (await login('viewer', 'viewer')).token;
  await cleanup();
});

afterAll(async () => {
  if (prisma) await cleanup();
  await app?.close();
});

beforeEach(() => {
  offset = 0;
});

describe('the unique index and the conditional writes, on real SQL', () => {
  it('many concurrent inserts of the same (node, kind) leave exactly one active row', async () => {
    const { nodeId } = await makeNode(0);
    const row = await prisma.localaiNode.findUniqueOrThrow({ where: { nodeId } });
    const input = { nodeRowId: row.id, kind: 'ark.drive_stale', code: 'LAI-CLD-022', severity: 'warning' as const, title: 'x', details: {} };
    const results = await Promise.all(Array.from({ length: 12 }, () => store.insert(input, new Date())));
    expect(results.filter((result) => result !== null)).toHaveLength(1);
    expect(await activeOf(nodeId)).toHaveLength(1);
  });

  it('a cleared alert frees the key: the same (node, kind) can be active again, as a new row', async () => {
    const { nodeId } = await makeNode(1);
    const row = await prisma.localaiNode.findUniqueOrThrow({ where: { nodeId } });
    const input = { nodeRowId: row.id, kind: 'sync.failed', code: 'LAI-CLD-023', severity: 'warning' as const, title: 'x', details: {} };
    const first = (await store.insert(input, new Date()))!;
    expect(await store.insert(input, new Date())).toBeNull();
    expect(await store.clear(first.id, new Date())).toBe(true);
    expect(await store.clear(first.id, new Date())).toBe(false);
    const second = await store.insert(input, new Date());
    expect(second).not.toBeNull();
    expect(second!.id).not.toBe(first.id);
    expect(await allOf(nodeId)).toHaveLength(2);
  });

  it('only one of two concurrent claims of a notification wins', async () => {
    const { nodeId } = await makeNode(2);
    const row = await prisma.localaiNode.findUniqueOrThrow({ where: { nodeId } });
    const alert = (await store.insert({ nodeRowId: row.id, kind: 'drill.failed', code: 'LAI-CLD-024', severity: 'critical', title: 'x', details: {} }, new Date()))!;
    const before = new Date(Date.now() - DAY);
    const wins = await Promise.all(Array.from({ length: 8 }, () => store.claimNotice(alert.id, new Date(), before)));
    expect(wins.filter(Boolean)).toHaveLength(1);
    expect(await store.claimNotice(alert.id, new Date(), before)).toBe(false);
  });
});

describe('a drive that has not been synced (ingest -> evaluate -> escalate -> clear)', () => {
  it('41 days: one active warning; 95 days: the SAME row becomes critical and is announced again with a mail; a newer sync clears it', async () => {
    const { nodeId } = await makeNode(3);
    const driveAge = (days: number) => (snapshot: LocalaiStateSnapshotV1) => {
      snapshot.ark.drive_last_synced_at = new Date(Date.now() - days * DAY - 3_600_000).toISOString();
    };

    await ingest(nodeId, driveAge(41));
    const first = await evaluate();
    expect(first.raised).toBeGreaterThanOrEqual(1);
    const [warning] = await activeOf(nodeId);
    expect(warning).toMatchObject({ kind: 'ark.drive_stale', code: 'LAI-CLD-022', severity: 'warning', title: 'Ark drive not synced for 41 days' });
    expect(warning!.notifiedAt).not.toBeNull();
    expect(await bellOf(nodeId, 'localai.alert_raised')).toHaveLength(1);
    const mailsBefore = await mailsOf('localai.alert_raised');

    // A second evaluation refreshes and says nothing.
    await evaluate();
    expect(await bellOf(nodeId, 'localai.alert_raised')).toHaveLength(1);
    expect(await mailsOf('localai.alert_raised')).toBe(mailsBefore);

    await ingest(nodeId, driveAge(95));
    await evaluate();
    const [critical, ...rest] = await activeOf(nodeId);
    expect(rest).toHaveLength(0);
    expect(critical).toMatchObject({ id: warning!.id, severity: 'critical', title: 'Ark drive not synced for 95 days' });
    const bells = await bellOf(nodeId, 'localai.alert_raised');
    expect(bells).toHaveLength(2);
    expect(bells[1]!.title).toContain('Ark drive not synced for 95 days');
    // The escalation is a critical alert, so the mail channel is now used; the warning never used it.
    expect(await mailsOf('localai.alert_raised')).toBeGreaterThan(mailsBefore);

    await ingest(nodeId, driveAge(1));
    await evaluate();
    expect(await activeOf(nodeId)).toHaveLength(0);
    const rows = await allOf(nodeId);
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ state: 'cleared' });
    expect(rows[0]!.clearedAt).not.toBeNull();
    expect(await bellOf(nodeId, 'localai.alert_cleared')).toHaveLength(1);
  });
});

describe('Alerts fire for a stale node (the epic success criterion)', () => {
  it('goes stale -> alert row + notification -> recovers -> cleared + resolved', async () => {
    const { nodeId } = await makeNode(4);
    await ingest(nodeId);
    await evaluate();
    expect(await activeOf(nodeId)).toHaveLength(0);

    // 15 minutes without a snapshot or a heartbeat, while the last mode was online.
    await goQuiet(nodeId, 15 * MIN);
    await evaluate();
    const stale = await activeOf(nodeId);
    expect(stale.map((alert) => [alert.kind, alert.severity, alert.code])).toEqual([['node.stale', 'warning', 'LAI-CLD-020']]);
    const bells = await bellOf(nodeId, 'localai.alert_raised');
    expect(bells).toHaveLength(1);
    expect(bells[0]).toMatchObject({ link: '/lab/alerts' });

    // Still silent at the next tick: refreshed, not announced again.
    await goQuiet(nodeId, 20 * MIN);
    await evaluate();
    expect(await bellOf(nodeId, 'localai.alert_raised')).toHaveLength(1);
    expect((await activeOf(nodeId))[0]!.title).toBe('Not seen for 20 min');

    // The node comes back: a fresh snapshot and a heartbeat.
    await ingest(nodeId);
    await prisma.workerNode.updateMany({ where: { name: nodeId }, data: { lastHeartbeatAt: new Date() } });
    await evaluate();
    expect(await activeOf(nodeId)).toHaveLength(0);
    expect((await allOf(nodeId))[0]).toMatchObject({ kind: 'node.stale', state: 'cleared' });
    expect(await bellOf(nodeId, 'localai.alert_cleared')).toHaveLength(1);
  });

  it('a node that goes to Offline mode is expected silence: nothing is raised, and an active alert clears at once', async () => {
    const { nodeId } = await makeNode(5);
    await ingest(nodeId);
    await goQuiet(nodeId, 15 * MIN);
    await evaluate();
    expect((await activeOf(nodeId)).map((alert) => alert.kind)).toEqual(['node.stale']);

    // The node reports it is switching to Offline mode: its last snapshot says so, and the ingestion queues a one-node job.
    await ingest(nodeId, (snapshot) => {
      snapshot.mode = 'offline';
    });
    await goQuiet(nodeId, 3 * 3_600_000);
    const queued = await prisma.job.findMany({ where: { type: 'localai.alerts.evaluate', subjectId: nodeId } });
    expect(queued).toHaveLength(1);
    expect(queued[0]!.payload).toEqual({ nodeId });
    expect(queued[0]).toMatchObject({ subjectType: 'localai_node', orgId: null });

    // The queue runs that job: this node only.
    await app.get(AlertsEvaluateHandler).process(queued[0] as never);
    await notifications.flush();
    expect(await activeOf(nodeId)).toHaveLength(0);
    expect((await allOf(nodeId))[0]).toMatchObject({ kind: 'node.stale', state: 'cleared' });

    // And it stays quiet at the 5-minute tick, hours later.
    await evaluate();
    expect(await activeOf(nodeId)).toHaveLength(0);
  });
});

describe('concurrent evaluations', () => {
  it('eight evaluations at once leave one active alert per kind and send one notification each', async () => {
    const { nodeId } = await makeNode(6);
    await ingest(nodeId, (snapshot) => {
      snapshot.ark.drive_last_synced_at = ago(50 * DAY).toISOString();
      snapshot.alerts = [{ code: 'LAI-HW-020', severity: 'critical', title: 'The laptop will sleep when the lid is closed' }];
    });
    await Promise.all(Array.from({ length: 8 }, () => service.evaluateAll()));
    await notifications.flush();
    const active = await activeOf(nodeId);
    expect(active.map((alert) => alert.kind)).toEqual(['ark.drive_stale', 'node.alerts.LAI-HW-020']);
    expect(await allOf(nodeId)).toHaveLength(2);
    expect(await bellOf(nodeId, 'localai.alert_raised')).toHaveLength(2);
  });
});

describe('acknowledge and repeat', () => {
  it('an unacknowledged alert is announced again after alerts.repeatHours; an acknowledged one never is', async () => {
    const { nodeId } = await makeNode(7);
    await ingest(nodeId, (snapshot) => {
      snapshot.ark.drive_last_synced_at = ago(45 * DAY).toISOString();
    });
    await evaluate();
    const [alert] = await activeOf(nodeId);
    expect(await bellOf(nodeId, 'localai.alert_raised')).toHaveLength(1);

    // 25 hours later (the node reported again meanwhile): the reminder goes out.
    offset = 25 * 3_600_000;
    await ingest(nodeId, (snapshot) => {
      snapshot.ark.drive_last_synced_at = ago(45 * DAY).toISOString();
    });
    await prisma.localaiNode.update({ where: { nodeId }, data: { lastStateAt: new Date(Date.now() + offset) } });
    await prisma.workerNode.updateMany({ where: { name: nodeId }, data: { lastHeartbeatAt: new Date(Date.now() + offset) } });
    await evaluate();
    expect(await bellOf(nodeId, 'localai.alert_raised')).toHaveLength(2);

    // A viewer cannot acknowledge; an operator can; it stays active.
    expect((await app.inject({ method: 'POST', url: `/api/localai/alerts/${alert!.id}/ack`, headers: { authorization: `Bearer ${viewerToken}` } })).statusCode).toBe(403);
    const acked = await app.inject({ method: 'POST', url: `/api/localai/alerts/${alert!.id}/ack`, headers: { authorization: `Bearer ${operatorToken}` } });
    expect(acked.statusCode).toBe(200);
    expect((acked.json() as { data: { state: string; acknowledgedAt: string } }).data).toMatchObject({ state: 'active', acknowledgedAt: expect.any(String) });
    const audit = await prisma.auditEvent.findMany({ where: { action: 'localai.alert.acknowledged', targetId: alert!.id } });
    expect(audit).toHaveLength(1);
    expect(audit[0]!.meta).toEqual({ kind: 'ark.drive_stale', nodeId });
    const row = await prisma.localaiAlert.findUniqueOrThrow({ where: { id: alert!.id } });
    expect(row).toMatchObject({ state: 'active', acknowledgedById: operatorId });

    // 3 days on: still silent, no reminder.
    offset = 3 * DAY;
    await prisma.localaiNode.update({ where: { nodeId }, data: { lastStateAt: new Date(Date.now() + offset) } });
    await prisma.workerNode.updateMany({ where: { name: nodeId }, data: { lastHeartbeatAt: new Date(Date.now() + offset) } });
    await evaluate();
    expect(await bellOf(nodeId, 'localai.alert_raised')).toHaveLength(2);
  });
});

describe('the alerts list over HTTP', () => {
  it('lists the cleared alerts of a node on request and the active ones most urgent first; a viewer may read', async () => {
    // Back to the real clock: the alerts the +25 h and +3 days evaluations of the previous test raised on every node clear.
    await evaluate();
    const response = await app.inject({ method: 'GET', url: `/api/localai/alerts?state=cleared&nodeId=${IDS[3]}`, headers: { authorization: `Bearer ${viewerToken}` } });
    expect(response.statusCode).toBe(200);
    const { data } = response.json() as { data: { alerts: Array<{ nodeId: string; state: string; fix: string; code: string }> } };
    expect(data.alerts.length).toBeGreaterThanOrEqual(1);
    expect(data.alerts.every((alert) => alert.nodeId === IDS[3])).toBe(true);
    expect(data.alerts.map((alert) => alert.state)).toEqual(Array(data.alerts.length).fill('cleared'));
    expect(data.alerts.some((alert) => alert.code === 'LAI-CLD-022')).toBe(true);
    expect(data.alerts.find((alert) => alert.code === 'LAI-CLD-022')!.fix).toContain('AI-ARK drive');

    const active = await app.inject({ method: 'GET', url: `/api/localai/alerts?nodeId=${IDS[6]}`, headers: { authorization: `Bearer ${viewerToken}` } });
    const severities = (active.json() as { data: { alerts: Array<{ severity: string }> } }).data.alerts.map((alert) => alert.severity);
    expect(severities).toEqual(['critical', 'warning']);
  });
});

describe('external alerts and retired nodes on real SQL', () => {
  it('endpoint.down is raised and cleared by its owner only; the evaluator leaves it alone', async () => {
    const nodeId = IDS[3]!;
    expect(await service.raiseExternal(nodeId, 'endpoint.down', { code: 'LAI-CLD-027', severity: 'warning', title: `${nodeId}.example.test is not answering` })).toBe(true);
    expect(await service.raiseExternal(nodeId, 'endpoint.down', { code: 'LAI-CLD-027', severity: 'warning', title: `${nodeId}.example.test is not answering` })).toBe(false);
    await evaluate();
    expect((await activeOf(nodeId)).map((alert) => alert.kind)).toEqual(['endpoint.down']);
    expect(await service.clearExternal(nodeId, 'endpoint.down')).toBe(true);
    expect(await activeOf(nodeId)).toHaveLength(0);
  });

  it('retiring a node clears every active alert it had', async () => {
    const nodeId = IDS[6]!;
    expect((await activeOf(nodeId)).length).toBeGreaterThan(0);
    await prisma.localaiNode.update({ where: { nodeId }, data: { phase: 'retired', retiredAt: new Date() } });
    await evaluate();
    expect(await activeOf(nodeId)).toHaveLength(0);
  });
});
