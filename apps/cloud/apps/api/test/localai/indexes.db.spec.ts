// The db tier for the Local AI schema (CLOUD-04): the three raw-SQL partial unique indexes of migration
// `add_localai` (one approved catalog, one active command per dedup key, one active alert per node and kind) and the
// worker-node settings the seed writes. Every case runs in a transaction that is rolled back, so the shared test
// database is left exactly as it was; a violation inside a case is caught behind a SAVEPOINT so the case can go on.
import { buildDatabaseUrl } from '@marinoscar/platform-api/core';
import { PrismaPg } from '@prisma/adapter-pg';
import { Prisma, PrismaClient } from '@prisma/client';

import { LOCALAI_NODES_SETTINGS, seedLocalaiNodeSettings } from '../../src/localai/localai.seed';

type Tx = Prisma.TransactionClient;

const prisma = new PrismaClient({ adapter: new PrismaPg(process.env.DATABASE_URL ?? buildDatabaseUrl(process.env)) });
const run = `${Date.now()}`;

class Rollback extends Error {}

/** Runs `fn` in a transaction that is always rolled back. */
async function scratch(fn: (tx: Tx) => Promise<void>): Promise<void> {
  try {
    await prisma.$transaction(async (tx) => {
      await fn(tx);
      throw new Rollback();
    });
  } catch (error) {
    if (!(error instanceof Rollback)) throw error;
  }
}

/** Asserts `fn` violates a unique index (naming `index`), then undoes it so the transaction stays usable. */
async function expectUniqueViolation(tx: Tx, index: string, fn: () => Promise<unknown>): Promise<void> {
  await tx.$executeRawUnsafe('SAVEPOINT expect_violation');
  let failure: unknown;
  try {
    await fn();
  } catch (error) {
    failure = error;
  }
  await tx.$executeRawUnsafe('ROLLBACK TO SAVEPOINT expect_violation');
  expect(failure).toBeDefined();
  expect(String((failure as Error).message)).toContain(index);
}

let counter = 0;
const unique = (prefix: string): string => `${prefix}-${run}-${counter++}`;

async function node(tx: Tx) {
  return tx.localaiNode.create({
    data: { nodeId: unique('ai-n'), displayName: 'Node Alpha', publicHost: 'node-alpha.example.test', servePattern: 'none' },
  });
}

const catalog = (tx: Tx, status: 'draft' | 'approved' | 'superseded' | 'valid') =>
  tx.localaiCatalogVersion.create({
    data: { version: unique('2026.11.04'), source: 'editor', status, files: {}, bundleSha256: 'sha256-placeholder' },
  });

const command = (tx: Tx, nodeId: string, status: 'queued' | 'running' | 'succeeded' | 'failed', dedupKey: string | null) =>
  tx.localaiCommand.create({
    data: { nodeId, type: 'localai.model.load', params: {}, status, dedupKey, expiresAt: new Date(Date.now() + 60_000) },
  });

const alert = (tx: Tx, nodeId: string | null, kind: string, state: 'active' | 'cleared') =>
  tx.localaiAlert.create({ data: { nodeId, kind, code: 'LAI-CLD-004', severity: 'warning', state, title: 'Node Alpha is quiet' } });

afterAll(async () => {
  await prisma.$disconnect();
});

describe('localai_catalog_one_approved', () => {
  it('refuses a second approved catalog version', async () => {
    await scratch(async (tx) => {
      await catalog(tx, 'approved');
      await expectUniqueViolation(tx, 'localai_catalog_one_approved', () => catalog(tx, 'approved'));
    });
  });

  it('allows any number of non-approved versions beside the approved one', async () => {
    await scratch(async (tx) => {
      await catalog(tx, 'approved');
      await catalog(tx, 'draft');
      await catalog(tx, 'valid');
      await catalog(tx, 'superseded');
      await catalog(tx, 'superseded');
      expect(await tx.localaiCatalogVersion.count({ where: { status: 'approved', version: { contains: run } } })).toBe(1);
    });
  });

  it('allows a new approval once the previous one is superseded', async () => {
    await scratch(async (tx) => {
      const first = await catalog(tx, 'approved');
      await tx.localaiCatalogVersion.update({ where: { id: first.id }, data: { status: 'superseded' } });
      await expect(catalog(tx, 'approved')).resolves.toMatchObject({ status: 'approved' });
    });
  });
});

describe('localai_commands_active_dedup', () => {
  it('refuses a second queued command with the same (node, dedup key)', async () => {
    await scratch(async (tx) => {
      const { id } = await node(tx);
      await command(tx, id, 'queued', 'localai.model.load:model-a');
      await expectUniqueViolation(tx, 'localai_commands_active_dedup', () => command(tx, id, 'queued', 'localai.model.load:model-a'));
    });
  });

  it('counts a running command as active', async () => {
    await scratch(async (tx) => {
      const { id } = await node(tx);
      await command(tx, id, 'running', 'localai.model.load:model-a');
      await expectUniqueViolation(tx, 'localai_commands_active_dedup', () => command(tx, id, 'queued', 'localai.model.load:model-a'));
    });
  });

  it('allows the same pair once the first command has settled', async () => {
    await scratch(async (tx) => {
      const { id } = await node(tx);
      const first = await command(tx, id, 'queued', 'localai.model.load:model-a');
      await tx.localaiCommand.update({ where: { id: first.id }, data: { status: 'succeeded' } });
      await expect(command(tx, id, 'queued', 'localai.model.load:model-a')).resolves.toMatchObject({ status: 'queued' });
      // A settled duplicate never conflicts either, whatever its outcome.
      await command(tx, id, 'failed', 'localai.model.load:model-a');
      await command(tx, id, 'succeeded', 'localai.model.load:model-a');
    });
  });

  it('never conflicts on a null dedup key', async () => {
    await scratch(async (tx) => {
      const { id } = await node(tx);
      await command(tx, id, 'queued', null);
      await command(tx, id, 'queued', null);
      await command(tx, id, 'running', null);
      expect(await tx.localaiCommand.count({ where: { nodeId: id } })).toBe(3);
    });
  });

  it('keys the dedup on the node: the same key may be active on two nodes', async () => {
    await scratch(async (tx) => {
      const a = await node(tx);
      const b = await node(tx);
      await command(tx, a.id, 'queued', 'localai.model.load:model-a');
      await expect(command(tx, b.id, 'queued', 'localai.model.load:model-a')).resolves.toBeDefined();
    });
  });
});

describe('localai_alerts_one_active', () => {
  it('refuses a second active alert of the same kind on a node', async () => {
    await scratch(async (tx) => {
      const { id } = await node(tx);
      await alert(tx, id, 'node.stale', 'active');
      await expectUniqueViolation(tx, 'localai_alerts_one_active', () => alert(tx, id, 'node.stale', 'active'));
    });
  });

  it('allows other kinds, other nodes, and a lab-wide alert of the same kind', async () => {
    await scratch(async (tx) => {
      const a = await node(tx);
      const b = await node(tx);
      await alert(tx, a.id, 'node.stale', 'active');
      await alert(tx, a.id, 'node.offline', 'active');
      await alert(tx, b.id, 'node.stale', 'active');
      await alert(tx, null, 'node.stale', 'active');
    });
  });

  it('refuses two active lab-wide alerts (node_id null) of the same kind', async () => {
    await scratch(async (tx) => {
      await alert(tx, null, 'sync.failed', 'active');
      await expectUniqueViolation(tx, 'localai_alerts_one_active', () => alert(tx, null, 'sync.failed', 'active'));
    });
  });

  it('allows any number of cleared duplicates, beside one active alert', async () => {
    await scratch(async (tx) => {
      const { id } = await node(tx);
      await alert(tx, id, 'node.stale', 'cleared');
      await alert(tx, id, 'node.stale', 'cleared');
      await alert(tx, id, 'node.stale', 'active');
      await alert(tx, null, 'sync.failed', 'cleared');
      await alert(tx, null, 'sync.failed', 'cleared');
      await alert(tx, null, 'sync.failed', 'active');
    });
  });
});

describe('worker-node settings seed', () => {
  const stepContext = { log: { info: () => undefined }, input: {}, platform: {} } as unknown as Parameters<typeof seedLocalaiNodeSettings.run>[1];

  it('left the three values in the global settings row (npm run prisma:seed)', async () => {
    const row = await prisma.systemSettings.findUnique({ where: { key: 'global' } });
    expect((row?.value as { nodes?: unknown } | undefined)?.nodes).toMatchObject({
      staleHeartbeatSeconds: 45,
      offlineStaleMultiplier: 4,
      offlineRetentionDays: 3650,
    });
    expect(LOCALAI_NODES_SETTINGS).toEqual({ staleHeartbeatSeconds: 45, offlineStaleMultiplier: 4, offlineRetentionDays: 3650 });
  });

  it('is idempotent: a second run changes nothing', async () => {
    await scratch(async (tx) => {
      await seedLocalaiNodeSettings.run(tx, stepContext);
      const before = await tx.systemSettings.findUniqueOrThrow({ where: { key: 'global' } });
      await expect(seedLocalaiNodeSettings.run(tx, stepContext)).resolves.toEqual({ created: 0, updated: 0 });
      const after = await tx.systemSettings.findUniqueOrThrow({ where: { key: 'global' } });
      expect(after.value).toEqual(before.value);
      expect(after.version).toBe(before.version);
    });
  });

  it('fills only the missing keys and never overwrites a value an administrator set', async () => {
    await scratch(async (tx) => {
      const row = await tx.systemSettings.findUniqueOrThrow({ where: { key: 'global' } });
      const value = row.value as Record<string, unknown>;
      await tx.systemSettings.update({
        where: { key: 'global' },
        data: { value: { ...value, nodes: { staleHeartbeatSeconds: 120, jobSecretBrokerEnabled: true } } },
      });

      await expect(seedLocalaiNodeSettings.run(tx, stepContext)).resolves.toEqual({ created: 0, updated: 1 });

      const after = await tx.systemSettings.findUniqueOrThrow({ where: { key: 'global' } });
      expect((after.value as { nodes: unknown }).nodes).toEqual({
        staleHeartbeatSeconds: 120,
        jobSecretBrokerEnabled: true,
        offlineStaleMultiplier: 4,
        offlineRetentionDays: 3650,
      });
      // The other namespaces of the row are untouched.
      expect({ ...(after.value as object), nodes: undefined }).toEqual({ ...value, nodes: undefined });
    });
  });
});
