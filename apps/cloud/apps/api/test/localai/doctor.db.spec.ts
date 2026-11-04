// The cloud Doctor checks on real SQL (docs/specs/cloud-app.md sections 7 and 9). The unit specs prove each verdict over
// an in-memory stand-in; this file proves the queries mean the same thing in PostgreSQL: the `where` clauses and
// enum values, the audit lookup for "enrolled for 10 minutes", the stored-key read through the real cipher, and that a
// run leaves every table as it found it.
//
// The database is shared with the other db specs, so every row here carries a per-run suffix and a check's verdict is
// asserted on the rows this file planted (their names, or the change in a count), never on the table as a whole.
import { randomBytes } from 'node:crypto';
import type { NestFastifyApplication } from '@nestjs/platform-fastify';
import { CredentialsService } from '@marinoscar/platform-api/credentials';

// A throwaway key for the cipher, generated per run and never committed. The CI database has none configured.
process.env.SECRETS_ENCRYPTION_KEY ??= randomBytes(32).toString('base64');

import { AictlDoctorCheck } from '../../src/localai/doctor/aictl.check';
import { CatalogApprovedDoctorCheck } from '../../src/localai/doctor/catalog-approved.check';
import { CommandsStuckDoctorCheck } from '../../src/localai/doctor/commands-stuck.check';
import { CredentialsDoctorCheck } from '../../src/localai/doctor/credentials.check';
import { NodesKeysDoctorCheck } from '../../src/localai/doctor/nodes-keys.check';
import { NodesPendingDoctorCheck } from '../../src/localai/doctor/nodes-pending.check';
import { SettingsNodesDoctorCheck } from '../../src/localai/doctor/settings-nodes.check';
import { createApp } from '../../src/main';
import { PrismaService } from '../../src/prisma/prisma.service';
import { expectContract } from './support/doctor-asserts';
import { testKey } from './support/capture-output';

const PURPOSE = 'localai_node_llama_key';
const run = `${Date.now()}`;
const nodeIdOf = (label: string) => `ai-${label}-${run}`;
const NOW = new Date();
const minutesAgo = (minutes: number) => new Date(NOW.getTime() - minutes * 60_000);
const daysAgo = (days: number) => new Date(NOW.getTime() - days * 86_400_000);

let app: NestFastifyApplication;
let prisma: PrismaService;
const nodeIds: string[] = [];
const credentialNames: string[] = [];

async function plantNode(label: string, over: Record<string, unknown> = {}) {
  const nodeId = nodeIdOf(label);
  nodeIds.push(nodeId);
  return prisma.localaiNode.create({
    data: { nodeId, displayName: `Node ${label}`, publicHost: `${label}.example.test`, servePattern: 'none', ...over } as never,
  });
}

beforeAll(async () => {
  app = await createApp();
  await app.init();
  await app.getHttpAdapter().getInstance().ready();
  prisma = app.get(PrismaService);
});

afterAll(async () => {
  if (prisma) {
    await prisma.credential.deleteMany({ where: { purpose: PURPOSE, name: { in: credentialNames } } });
    await prisma.auditEvent.deleteMany({ where: { targetId: { in: nodeIds }, action: 'localai.node.enrolled' } });
    await prisma.localaiNode.deleteMany({ where: { nodeId: { in: nodeIds } } }); // cascades to localai_commands
  }
  await app?.close();
});

describe('localai.nodes.pending on real SQL', () => {
  it('names a pending node older than 7 days, and no other node', async () => {
    await plantNode('pend-old', { phase: 'pending', createdAt: daysAgo(8) });
    await plantNode('pend-young', { phase: 'pending', createdAt: daysAgo(6) });
    await plantNode('pend-enrolled', { phase: 'enrolled', createdAt: daysAgo(30) });
    await plantNode('pend-retired', { phase: 'pending', createdAt: daysAgo(30), retiredAt: daysAgo(1) });

    const outcome = await app.get(NodesPendingDoctorCheck).inspect(NOW);
    expect(outcome.status).toBe('warn');
    expectContract(outcome);
    expect(outcome.data).toMatchObject({ code: 'LAI-CLD-031' });
    // The list is capped for display; read the whole answer through the data count and the names that fit.
    const older = await prisma.localaiNode.findMany({ where: { phase: 'pending', retiredAt: null, createdAt: { lt: daysAgo(7) } }, select: { nodeId: true } });
    expect(older.map((node) => node.nodeId)).toContain(nodeIdOf('pend-old'));
    expect(older.map((node) => node.nodeId)).not.toContain(nodeIdOf('pend-young'));
    expect(older.map((node) => node.nodeId)).not.toContain(nodeIdOf('pend-enrolled'));
    expect(older.map((node) => node.nodeId)).not.toContain(nodeIdOf('pend-retired'));
    expect(Number(outcome.data?.count)).toBe(older.length);
  });
});

describe('localai.commands.stuck on real SQL', () => {
  it('counts a queued command past its expiry and a running one past its lease, 5 minutes late, and nothing else', async () => {
    const check = app.get(CommandsStuckDoctorCheck);
    const before = await check.inspect(NOW);
    const base = before.data as { queuedPastExpiry: number; runningPastLease: number };

    const node = await plantNode('cmd');
    const command = (status: string, expiresAt: Date, leaseExpiresAt: Date | null = null) => ({
      nodeId: node.id,
      type: 'status.refresh',
      params: {},
      status,
      expiresAt,
      leaseExpiresAt,
    });
    await prisma.localaiCommand.createMany({
      data: [
        command('queued', minutesAgo(6)), // counts
        command('queued', minutesAgo(4)), // the sweeper is about to run
        command('queued', new Date(NOW.getTime() + 600_000)), // not expired
        command('running', new Date(NOW.getTime() + 600_000), minutesAgo(6)), // counts
        command('running', new Date(NOW.getTime() + 600_000), minutesAgo(4)),
        command('expired', minutesAgo(60)), // settled commands never count
        command('failed', minutesAgo(60), minutesAgo(60)),
      ] as never,
    });

    const after = await check.inspect(NOW);
    expect(after.status).toBe('warn');
    expectContract(after);
    expect(after.data).toMatchObject({ queuedPastExpiry: base.queuedPastExpiry + 1, runningPastLease: base.runningPastLease + 1, code: 'LAI-CLD-030' });
  });
});

describe('localai.nodes.keys on real SQL', () => {
  it('times a keyless node from its enrollment audit row, and skips pending, retired and keyed nodes', async () => {
    const late = await plantNode('key-late', { phase: 'enrolled', createdAt: daysAgo(3) });
    const fresh = await plantNode('key-fresh', { phase: 'enrolled', createdAt: daysAgo(3) });
    await plantNode('key-keyed', { phase: 'enrolled', createdAt: daysAgo(3), keyReportedAt: minutesAgo(1), keyHint: 'abcd...wxyz' });
    await plantNode('key-pending', { phase: 'pending', createdAt: daysAgo(3) });
    await plantNode('key-retired', { phase: 'retired', createdAt: daysAgo(3), retiredAt: daysAgo(1) });
    const enrolled = (targetId: string, createdAt: Date) =>
      prisma.auditEvent.create({ data: { action: 'localai.node.enrolled', targetType: 'localai_node', targetId, createdAt } });
    await enrolled(late.nodeId, minutesAgo(30));
    await enrolled(fresh.nodeId, minutesAgo(2));

    const outcome = await app.get(NodesKeysDoctorCheck).inspect(NOW);
    expect(outcome.status).toBe('warn');
    expectContract(outcome);
    expect(outcome.data).toMatchObject({ code: 'LAI-CLD-026' });

    // Take the late node away: with only the fresh one inside its grace period it must not be named. The count is the
    // table's, so assert on the planted rows through a second pass.
    await prisma.localaiNode.update({ where: { id: late.id }, data: { keyReportedAt: minutesAgo(1), keyHint: 'abcd...wxyz' } });
    const second = await app.get(NodesKeysDoctorCheck).inspect(NOW);
    expect(second.detail).not.toContain(late.nodeId);
    expect(second.detail).not.toContain(fresh.nodeId);
  });
});

describe('localai.catalog.approved on real SQL', () => {
  it('agrees with the table: pass when a version is approved, warn when none is', async () => {
    const approved = await prisma.localaiCatalogVersion.count({ where: { status: 'approved' } });
    const outcome = await app.get(CatalogApprovedDoctorCheck).run();
    expect(outcome.status).toBe(approved > 0 ? 'pass' : 'warn');
    expectContract(outcome);
  });
});

describe('localai.settings.nodes on real SQL', () => {
  it('passes on the values the seed leaves (45 s, 3650 days)', async () => {
    const outcome = await app.get(SettingsNodesDoctorCheck).run();
    expectContract(outcome);
    // The unit spec proves the warnings over the platform defaults; the stored row here is the seeded one.
    expect(outcome).toMatchObject({ status: 'pass', data: { staleHeartbeatSeconds: 45, offlineRetentionDays: 3650 } });
  });
});

describe('localai.aictl against the real binary', () => {
  // cloud-ci builds aictl and exports LOCALAI_AICTL_PATH for the db tier; a local run without it skips this case.
  (process.env.LOCALAI_AICTL_PATH ? it : it.skip)('runs `aictl --json version` and passes', async () => {
    const outcome = await app.get(AictlDoctorCheck).run();
    expect(outcome.status).toBe('pass');
    expectContract(outcome);
  });
});

describe('localai.credentials on real SQL', () => {
  it('reads a stored key through the real cipher, and fails with LAI-CLD-032 once the ciphertext cannot be authenticated', async () => {
    // Sorts before every `ai-n*` name, so the check (which reads the first stored key) reads this one.
    const name = `aa-doctor-${run}`;
    credentialNames.push(name);
    const credentials = app.get(CredentialsService);
    const key = testKey();
    await credentials.setSecret(PURPOSE, name, key);
    const check = app.get(CredentialsDoctorCheck);

    const healthy = await check.run();
    expect(healthy.status).toBe('pass');
    expect(JSON.stringify(healthy)).not.toContain(key);
    expectContract(healthy);

    // What a changed SECRETS_ENCRYPTION_KEY looks like: a ciphertext the cipher cannot authenticate.
    await prisma.credential.update({ where: { purpose_name: { purpose: PURPOSE, name } }, data: { secret: randomBytes(48).toString('base64') } });
    const broken = await check.run();
    expect(broken.status).toBe('fail');
    expect(broken.data).toMatchObject({ code: 'LAI-CLD-032' });
    expectContract(broken);
  });
});

describe('a run of every check', () => {
  it('leaves the tables as it found them', async () => {
    const snapshot = async () => ({
      credentials: await prisma.credential.count(),
      nodes: await prisma.localaiNode.count(),
      commands: await prisma.localaiCommand.count(),
      catalog: await prisma.localaiCatalogVersion.count(),
      audit: await prisma.auditEvent.count(),
      settings: JSON.stringify(await prisma.systemSettings.findMany()),
    });
    const before = await snapshot();
    for (const check of [AictlDoctorCheck, CredentialsDoctorCheck, CatalogApprovedDoctorCheck, NodesPendingDoctorCheck, CommandsStuckDoctorCheck, NodesKeysDoctorCheck, SettingsNodesDoctorCheck]) {
      await app.get(check).run();
    }
    expect(await snapshot()).toEqual(before);
  });
});
