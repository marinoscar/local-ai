// The gateway tables against real PostgreSQL (gateway.md section 3.6, issue GW-01): what only real SQL proves.
//   - the unique token hash and the (key, day) primary key of the daily rollup refuse a duplicate;
//   - deleting a user through the platform's purge path deletes their keys and keeps the usage history with key_id NULL,
//     and the daily rollup, which has no foreign key, is untouched;
//   - the user-data export lists a user's keys and never carries the token hash;
//   - a model policy row defaults to enabled, no on-demand load, a 120 s load wait.
// Cases that expect a violation run behind a SAVEPOINT in a transaction that is rolled back, so the shared test database
// is left as it was; the purge case creates and removes its own users.
import { randomBytes, randomUUID } from 'node:crypto';
import type { NestFastifyApplication } from '@nestjs/platform-fastify';
import { buildDatabaseUrl } from '@marinoscar/platform-api/core';
import { userDataTables } from '@marinoscar/platform-api/exports';
import type { ExportDatamodel } from '@marinoscar/platform-api/exports';
import { UserPurgeRunner } from '@marinoscar/platform-api/user-data';
import { PrismaPg } from '@prisma/adapter-pg';
import { Prisma, PrismaClient } from '@prisma/client';

import { GATEWAY_KEY_EXPORT_FIELDS } from '../../src/localai/gateway/gateway.user-data';
import { createApp } from '../../src/main';

type Tx = Prisma.TransactionClient;

const prisma = new PrismaClient({ adapter: new PrismaPg(process.env.DATABASE_URL ?? buildDatabaseUrl(process.env)) });
const run = `${Date.now()}`;
const emails = [`gwmodel-a-${run}@example.test`, `gwmodel-b-${run}@example.test`, `gwmodel-d-${run}@example.test`];
// A token hash is 64 hex characters; build them at run time so no literal sits in the source.
const hash = () => randomBytes(32).toString('hex');

let app: NestFastifyApplication;
const userIds: string[] = [];
// Key ids whose daily rollup row a case left behind (the rollup has no foreign key, so a key delete does not take it).
const rollupKeyIds: string[] = [];

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

/** Asserts `fn` is refused with Prisma error `code`, then undoes it so the transaction stays usable. */
async function expectRefused(tx: Tx, code: string, fn: () => Promise<unknown>): Promise<void> {
  await tx.$executeRawUnsafe('SAVEPOINT expect_refused');
  let failure: unknown;
  try {
    await fn();
  } catch (error) {
    failure = error;
  }
  await tx.$executeRawUnsafe('ROLLBACK TO SAVEPOINT expect_refused');
  expect(failure).toMatchObject({ code });
}

async function login(email: string): Promise<string> {
  const response = await app.inject({ method: 'POST', url: '/api/auth/test/login', payload: { email, role: 'contributor' } });
  expect(response.statusCode).toBeLessThan(400);
  const user = await prisma.user.findFirstOrThrow({ where: { email } });
  return user.id;
}

const keyData = (userId: string, name: string, overrides: Partial<Prisma.LocalaiGatewayKeyUncheckedCreateInput> = {}): Prisma.LocalaiGatewayKeyUncheckedCreateInput => ({
  userId,
  name,
  tokenHash: hash(),
  tokenPrefix: 'lai_ab12cd',
  allowedModels: ['lab-coder'],
  rpmLimit: 60,
  concurrentLimit: 4,
  expiresAt: new Date(Date.now() + 86_400_000),
  ...overrides,
});

const usageData = (keyId: string | null, userId: string | null): Prisma.LocalaiGatewayUsageUncheckedCreateInput => ({
  keyId,
  userId,
  requestId: randomUUID(),
  endpoint: 'chat.completions',
  model: 'qwen3.6-35b-a3b-q4km',
  requestedModel: 'lab-coder',
  nodeId: 'ai-n3',
  status: 200,
  stream: true,
  usageSource: 'upstream',
  promptTokens: 12,
  completionTokens: 34,
  durationMs: 850,
});

beforeAll(async () => {
  app = await createApp();
  await app.init();
  await app.getHttpAdapter().getInstance().ready();
  for (const email of emails.slice(0, 2)) userIds.push(await login(email));
});

afterAll(async () => {
  if (userIds.length > 0) {
    const keys = await prisma.localaiGatewayKey.findMany({ where: { userId: { in: userIds } }, select: { id: true } });
    await prisma.localaiGatewayUsage.deleteMany({ where: { userId: { in: userIds } } });
    await prisma.localaiGatewayUsageDaily.deleteMany({ where: { keyId: { in: [...keys.map((key) => key.id), ...rollupKeyIds] } } });
    await prisma.localaiGatewayKey.deleteMany({ where: { userId: { in: userIds } } });
  }
  await prisma.user.deleteMany({ where: { email: { in: emails } } }).catch(() => undefined);
  await app?.close();
  await prisma.$disconnect();
});

describe('the gateway key table', () => {
  it('applies the spec defaults and stores the limits, the budget and the model list', async () => {
    await scratch(async (tx) => {
      const key = await tx.localaiGatewayKey.create({ data: keyData(userIds[0]!, 'Desk laptop', { dailyTokenBudget: 5_000_000n, allowedModels: [] }) });
      expect(key).toMatchObject({ kind: 'user', allowedModels: [], rpmLimit: 60, concurrentLimit: 4, dailyTokenBudget: 5_000_000n, revokedAt: null, lastUsedAt: null, createdById: null });
      expect(key.createdAt).toBeInstanceOf(Date);
    });
  });

  it('refuses a second key with the same token hash (unique violation)', async () => {
    await scratch(async (tx) => {
      const shared = hash();
      await tx.localaiGatewayKey.create({ data: keyData(userIds[0]!, 'First', { tokenHash: shared }) });
      await expectRefused(tx, 'P2002', () => tx.localaiGatewayKey.create({ data: keyData(userIds[1]!, 'Second', { tokenHash: shared }) }));
    });
  });

  it('refuses a key for a user that does not exist', async () => {
    await scratch(async (tx) => {
      await expectRefused(tx, 'P2003', () => tx.localaiGatewayKey.create({ data: keyData(randomUUID(), 'Ghost') }));
    });
  });
});

describe('the daily usage rollup', () => {
  it('refuses a second row for the same (key, day) and accepts another day or key', async () => {
    await scratch(async (tx) => {
      const keyA = randomUUID();
      const keyB = randomUUID();
      const day = new Date('2026-11-04T00:00:00.000Z');
      await tx.localaiGatewayUsageDaily.create({ data: { keyId: keyA, day, requests: 3, promptTokens: 100n, completionTokens: 200n } });
      await expectRefused(tx, 'P2002', () => tx.localaiGatewayUsageDaily.create({ data: { keyId: keyA, day } }));
      await tx.localaiGatewayUsageDaily.create({ data: { keyId: keyA, day: new Date('2026-11-05T00:00:00.000Z') } });
      await tx.localaiGatewayUsageDaily.create({ data: { keyId: keyB, day } });
      const row = await tx.localaiGatewayUsageDaily.findUniqueOrThrow({ where: { keyId_day: { keyId: keyA, day } } });
      expect(row).toMatchObject({ requests: 3, promptTokens: 100n, completionTokens: 200n });
      expect((await tx.localaiGatewayUsageDaily.findUniqueOrThrow({ where: { keyId_day: { keyId: keyB, day } } })).requests).toBe(0);
    });
  });
});

describe('the model policy table', () => {
  it('defaults to enabled, no on-demand load and a 120 s load wait', async () => {
    await scratch(async (tx) => {
      const policy = await tx.localaiModelPolicy.create({ data: { modelId: `model-${run}`, aliases: ['lab-coder'], preferredNodes: ['ai-n3', 'ai-n1'] } });
      expect(policy).toMatchObject({ enabled: true, onDemandLoad: false, loadWaitS: 120, firstByteTimeoutS: null, aliases: ['lab-coder'], preferredNodes: ['ai-n3', 'ai-n1'], updatedById: null });
      expect(policy.updatedAt).toBeInstanceOf(Date);
    });
  });
});

describe('deleting a user (the platform purge path)', () => {
  it('deletes their keys and keeps the usage rows with key_id NULL; the rollup and the other user\'s key are untouched', async () => {
    const [owner, other] = userIds as [string, string];
    const keyOne = await prisma.localaiGatewayKey.create({ data: keyData(owner, 'Laptop') });
    const keyTwo = await prisma.localaiGatewayKey.create({ data: keyData(owner, 'Phone', { kind: 'service', expiresAt: null }) });
    const otherKey = await prisma.localaiGatewayKey.create({ data: keyData(other, 'Other') });
    const requestIds = [randomUUID(), randomUUID(), randomUUID()];
    await prisma.localaiGatewayUsage.createMany({
      data: [
        { ...usageData(keyOne.id, owner), requestId: requestIds[0]! },
        { ...usageData(keyTwo.id, owner), requestId: requestIds[1]! },
        { ...usageData(otherKey.id, other), requestId: requestIds[2]! },
      ],
    });
    await prisma.localaiGatewayUsageDaily.create({ data: { keyId: keyOne.id, day: new Date('2026-11-04T00:00:00.000Z'), requests: 1 } });
    rollupKeyIds.push(keyOne.id);

    const counts = await app.get(UserPurgeRunner, { strict: false }).purgeAndRemoveUser(owner, randomUUID());
    expect(counts.usersDeleted).toBe(1);

    expect(await prisma.user.count({ where: { id: owner } })).toBe(0);
    expect(await prisma.localaiGatewayKey.count({ where: { userId: owner } })).toBe(0);
    const usage = await prisma.localaiGatewayUsage.findMany({ where: { requestId: { in: requestIds } }, orderBy: { requestId: 'asc' } });
    expect(usage).toHaveLength(3);
    expect(usage.filter((row) => row.userId === owner).map((row) => row.keyId)).toEqual([null, null]);
    expect(usage.find((row) => row.requestId === requestIds[2])?.keyId).toBe(otherKey.id);
    expect(await prisma.localaiGatewayUsageDaily.count({ where: { keyId: keyOne.id } })).toBe(1);
    expect(await prisma.localaiGatewayKey.count({ where: { id: otherKey.id } })).toBe(1);
  });

  it('also deletes the keys when the user row itself is deleted (the foreign key cascades)', async () => {
    const email = `gwmodel-c-${run}@example.test`;
    const id = await login(email);
    const key = await prisma.localaiGatewayKey.create({ data: keyData(id, 'Cascade') });
    await prisma.localaiGatewayUsage.create({ data: usageData(key.id, id) });
    await prisma.user.delete({ where: { id } });
    expect(await prisma.localaiGatewayKey.count({ where: { id: key.id } })).toBe(0);
    expect(await prisma.localaiGatewayUsage.count({ where: { userId: id, keyId: null } })).toBe(1);
    await prisma.localaiGatewayUsage.deleteMany({ where: { userId: id } });
  });
});

describe('the user-data export', () => {
  it('lists a user\'s keys with the documented columns and never the token hash', async () => {
    const user = userIds[1]!;
    const secretHash = hash();
    // The purge case above left this user one key; start from none, and give another user a key that must not appear.
    await prisma.localaiGatewayKey.deleteMany({ where: { userId: user } });
    const stranger = await login(`gwmodel-d-${run}@example.test`);
    userIds.push(stranger);
    await prisma.localaiGatewayKey.create({ data: keyData(stranger, 'Stranger') });
    const key = await prisma.localaiGatewayKey.create({
      data: keyData(user, 'Exported', { tokenHash: secretHash, kind: 'service', expiresAt: null, dailyTokenBudget: null, revokedAt: new Date('2026-11-02T10:00:00.000Z') }),
    });
    const ctx = {
      exportId: randomUUID(),
      scope: 'user',
      subjectId: user,
      requestedById: user,
      db: prisma,
      datamodel: Prisma.dmmf.datamodel as unknown as ExportDatamodel,
      pageSize: 100,
      now: new Date(),
      timeZone: 'UTC',
    } as unknown as Parameters<typeof userDataTables>[0];

    let table: Awaited<ReturnType<typeof userDataTables> extends AsyncGenerator<infer T> ? T : never> | undefined;
    for await (const candidate of userDataTables(ctx)) {
      if (candidate.dataset === 'localai_gateway_key') table = candidate;
    }
    expect(table).toBeDefined();
    expect(table!.columns.map((column) => column.key)).toEqual([...GATEWAY_KEY_EXPORT_FIELDS]);

    const rows: Record<string, unknown>[] = [];
    for await (const row of table!.rows) rows.push(row as Record<string, unknown>);
    expect(rows).toHaveLength(1); // only this user's key: the stranger's is not in the file
    expect(rows[0]).toMatchObject({ id: key.id, name: 'Exported', kind: 'service', tokenPrefix: 'lai_ab12cd', allowedModels: JSON.stringify(['lab-coder']), expiresAt: null, revokedAt: '2026-11-02T10:00:00.000Z', lastUsedAt: null });
    expect(JSON.stringify(rows)).not.toContain(secretHash);
    expect(Object.keys(rows[0]!)).not.toContain('tokenHash');
  });
});
