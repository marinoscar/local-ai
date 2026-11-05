import 'reflect-metadata';

import { DAY_MS } from './usage-day';
import { USAGE_PURGE_TYPE, UsagePurgeHandler } from './usage-purge.job';
import { UsagePurgeTask } from './usage-purge.task';

const NOW = Date.UTC(2026, 10, 4, 3, 21);

function handler(retentionDays: number, rows: Array<{ id: string; createdAt: Date }>) {
  const prisma = {
    localaiGatewayUsage: {
      findMany: jest.fn(async ({ where, take }: any) => rows.filter((row) => row.createdAt < where.createdAt.lt).sort((a, b) => a.createdAt.getTime() - b.createdAt.getTime()).slice(0, take).map((row) => ({ id: row.id }))),
      deleteMany: jest.fn(async ({ where }: any) => {
        let count = 0;
        for (let index = rows.length - 1; index >= 0; index -= 1) {
          if (where.id.in.includes(rows[index]!.id)) {
            rows.splice(index, 1);
            count += 1;
          }
        }
        return { count };
      }),
    },
  };
  const registry = { register: jest.fn() };
  const settings = { readAll: jest.fn(async () => ({ gateway: { usageRetentionDays: retentionDays } })) };
  const job = new UsagePurgeHandler(registry as never, prisma as never, settings as never);
  job.now = () => NOW;
  return { job, prisma, registry, settings, rows };
}

describe('the usage purge job', () => {
  it('registers itself under the permanent type, server-only, with an execution profile', () => {
    const { job, registry } = handler(90, []);
    job.onModuleInit();
    expect(registry.register).toHaveBeenCalledWith(job);
    expect(job.type).toBe('localai.gateway.usage_purge');
    expect(USAGE_PURGE_TYPE).toBe('localai.gateway.usage_purge');
    expect(job.profile).toEqual({ maxRuntimeMs: 600_000, maxAttempts: 1 });
    expect('nodeResultSchema' in job).toBe(false);
    expect('persistNodeResult' in job).toBe(false);
  });

  it('deletes the rows older than the retention and keeps the rest, the boundary included', async () => {
    const age = (days: number, extraMs = 0) => new Date(NOW - days * DAY_MS - extraMs);
    const { job, rows } = handler(90, [
      { id: 'old', createdAt: age(91) },
      { id: 'just-old', createdAt: age(90, 1) },
      { id: 'on-the-line', createdAt: age(90) },
      { id: 'fresh', createdAt: age(1) },
      { id: 'now', createdAt: new Date(NOW) },
    ]);
    expect(await job.purge()).toBe(2);
    expect(rows.map((row) => row.id)).toEqual(['on-the-line', 'fresh', 'now']);
  });

  it('follows gateway.usageRetentionDays', async () => {
    const { job, rows } = handler(7, [
      { id: 'eight', createdAt: new Date(NOW - 8 * DAY_MS) },
      { id: 'six', createdAt: new Date(NOW - 6 * DAY_MS) },
    ]);
    expect(await job.purge()).toBe(1);
    expect(rows.map((row) => row.id)).toEqual(['six']);
  });

  it('works through a backlog in batches of 5000', async () => {
    const rows = Array.from({ length: 12_000 }, (_, index) => ({ id: `r${index}`, createdAt: new Date(NOW - 200 * DAY_MS + index) }));
    const { job, prisma } = handler(90, rows);
    expect(await job.purge()).toBe(12_000);
    expect(prisma.localaiGatewayUsage.deleteMany).toHaveBeenCalledTimes(3);
  });

  it('never touches the daily rollups (the handler has no access to that table)', async () => {
    const { job, prisma } = handler(90, []);
    await job.purge();
    expect(Object.keys(prisma)).toEqual(['localaiGatewayUsage']);
  });
});

describe('UsagePurgeTask', () => {
  it('is scheduled once a day', () => {
    const keys = Reflect.getMetadataKeys(UsagePurgeTask.prototype.handleCron);
    const values = keys.map((key) => Reflect.getMetadata(key, UsagePurgeTask.prototype.handleCron) as unknown);
    expect(JSON.stringify(values)).toContain('21 3 * * *');
  });

  it('only enqueues the purge job: a global housekeeping job with no organization', async () => {
    const jobs = { enqueue: jest.fn(async () => ({ id: 'job-1' })) };
    const prisma = { job: { findFirst: jest.fn(async () => null) } };
    await new UsagePurgeTask(jobs as never, prisma as never).handleCron();
    expect(jobs.enqueue).toHaveBeenCalledTimes(1);
    expect(jobs.enqueue).toHaveBeenCalledWith(expect.objectContaining({ type: USAGE_PURGE_TYPE, orgId: null }));
  });

  it('skips the tick while a purge is still pending or running, and never throws', async () => {
    const jobs = { enqueue: jest.fn(async () => ({ id: 'job-1' })) };
    const prisma = { job: { findFirst: jest.fn(async () => ({ id: 'job-0', status: 'running' })) } };
    const task = new UsagePurgeTask(jobs as never, prisma as never);
    await task.handleCron();
    expect(jobs.enqueue).not.toHaveBeenCalled();
    jobs.enqueue.mockRejectedValueOnce(new Error('queue down'));
    prisma.job.findFirst.mockResolvedValueOnce(null as never);
    await expect(task.handleCron()).resolves.toBeUndefined();
  });
});
