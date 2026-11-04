import { ResendKeysService } from './resend-keys.service';

describe('ResendKeysService', () => {
  const setup = (nodes: Array<{ nodeId: string }>) => {
    const prisma = { localaiNode: { findMany: jest.fn().mockResolvedValue(nodes) } };
    const enqueue = jest.fn(async (nodeId: string) => ({ id: `cmd-${nodeId}`, created: true }));
    return { prisma, enqueue, service: new ResendKeysService(prisma as never, { enqueue } as never) };
  };

  it('asks only enrolled nodes (pending and retired are not asked)', async () => {
    const { prisma, service } = setup([]);
    await service.resendAll('admin-1');
    expect(prisma.localaiNode.findMany).toHaveBeenCalledWith({ where: { phase: 'enrolled' }, select: { nodeId: true }, orderBy: { nodeId: 'asc' } });
  });

  it('queues one status.refresh per node as the system, on behalf of the admin, and returns the ids in node order', async () => {
    const { enqueue, service } = setup([{ nodeId: 'ai-n1' }, { nodeId: 'ai-n3' }]);
    const ids = await service.resendAll('admin-1');
    expect(ids).toEqual(['cmd-ai-n1', 'cmd-ai-n3']);
    expect(enqueue).toHaveBeenCalledTimes(2);
    expect(enqueue).toHaveBeenNthCalledWith(1, 'ai-n1', 'status.refresh', {}, { userId: 'admin-1' }, 'system');
    expect(enqueue).toHaveBeenNthCalledWith(2, 'ai-n3', 'status.refresh', {}, { userId: 'admin-1' }, 'system');
  });

  it('returns an empty list when no node is enrolled', async () => {
    const { enqueue, service } = setup([]);
    expect(await service.resendAll(null)).toEqual([]);
    expect(enqueue).not.toHaveBeenCalled();
  });
});
