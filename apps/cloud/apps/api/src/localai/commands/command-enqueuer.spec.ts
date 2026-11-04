import { Prisma } from '@prisma/client';

import { PrismaCommandEnqueuer } from './command-enqueuer';

function uniqueViolation() {
  return new Prisma.PrismaClientKnownRequestError('Unique constraint failed', { code: 'P2002', clientVersion: 'test' });
}

describe('PrismaCommandEnqueuer', () => {
  const setup = () => {
    const prisma = {
      localaiNode: { findUnique: jest.fn().mockResolvedValue({ id: 'row-ai-n3' }) },
      localaiCommand: { create: jest.fn().mockResolvedValue({ id: 'cmd-1' }), findFirst: jest.fn() },
    };
    return { prisma, enqueuer: new PrismaCommandEnqueuer(prisma as never) };
  };

  it('inserts a status.refresh with the dedup key, a two minute queued TTL and the actor', async () => {
    const { prisma, enqueuer } = setup();
    const before = Date.now();
    const result = await enqueuer.enqueue('ai-n3', 'status.refresh', {}, { userId: 'admin-1' }, 'system');
    expect(result).toEqual({ id: 'cmd-1', created: true });
    const data = prisma.localaiCommand.create.mock.calls[0][0].data;
    expect(data).toMatchObject({ nodeId: 'row-ai-n3', type: 'localai.status.refresh', params: {}, dedupKey: 'status.refresh:-', source: 'system', requestedById: 'admin-1' });
    expect(data.expiresAt.getTime() - before).toBeGreaterThanOrEqual(2 * 60_000);
    expect(data.expiresAt.getTime() - before).toBeLessThan(2 * 60_000 + 5_000);
  });

  it('returns the active command when the partial unique index refuses a duplicate', async () => {
    const { prisma, enqueuer } = setup();
    prisma.localaiCommand.create.mockRejectedValue(uniqueViolation());
    prisma.localaiCommand.findFirst.mockResolvedValue({ id: 'cmd-active' });
    expect(await enqueuer.enqueue('ai-n3', 'status.refresh', {}, { userId: null }, 'system')).toEqual({ id: 'cmd-active', created: false });
    expect(prisma.localaiCommand.findFirst.mock.calls[0][0].where).toEqual({ nodeId: 'row-ai-n3', dedupKey: 'status.refresh:-', status: { in: ['queued', 'running'] } });
  });

  it('rethrows any other database error', async () => {
    const { prisma, enqueuer } = setup();
    prisma.localaiCommand.create.mockRejectedValue(new Error('connection lost'));
    await expect(enqueuer.enqueue('ai-n3', 'status.refresh', {}, { userId: null }, 'system')).rejects.toThrow('connection lost');
  });

  it('refuses an unknown node and parameters the command does not take', async () => {
    const { prisma, enqueuer } = setup();
    await expect(enqueuer.enqueue('ai-n3', 'status.refresh', { x: 1 }, { userId: null }, 'system')).rejects.toThrow('no parameters');
    prisma.localaiNode.findUnique.mockResolvedValue(null);
    await expect(enqueuer.enqueue('ai-n9', 'status.refresh', {}, { userId: null }, 'system')).rejects.toThrow('Unknown Local AI node');
  });
});
