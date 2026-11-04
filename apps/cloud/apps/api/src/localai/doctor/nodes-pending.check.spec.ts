import { describeDoctorCheckConformance } from '@marinoscar/platform-api/doctor/testing';

import { fakePrisma } from '../../../test/localai/support/fake-tables';
import { expectContract, leakBait } from '../../../test/localai/support/doctor-asserts';
import { NodesPendingDoctorCheck } from './nodes-pending.check';

const registry = { register: jest.fn() };
const NOW = new Date('2026-11-10T12:00:00Z');
const daysAgo = (days: number) => new Date(NOW.getTime() - days * 86_400_000);
const checkWith = (nodes: Array<Record<string, unknown>>) => new NodesPendingDoctorCheck(registry as never, fakePrisma({ localaiNode: nodes }) as never);
const node = (nodeId: string, phase: string, createdAt: Date, retiredAt: Date | null = null) => ({ nodeId, phase, createdAt, retiredAt });

describe('localai.nodes.pending', () => {
  it('registers itself', () => {
    const check = checkWith([]);
    check.onModuleInit();
    expect(registry.register).toHaveBeenCalledWith(check);
  });

  it.each([
    ['no nodes at all', [], 'pass'],
    ['pending 6 days', [node('ai-n4', 'pending', daysAgo(6))], 'pass'],
    ['pending just under 7 days', [node('ai-n4', 'pending', new Date(NOW.getTime() - 7 * 86_400_000 + 1000))], 'pass'],
    ['pending 8 days', [node('ai-n4', 'pending', daysAgo(8))], 'warn'],
    ['enrolled 30 days (connected)', [node('ai-n4', 'enrolled', daysAgo(30))], 'pass'],
    ['retired 30 days', [node('ai-n4', 'retired', daysAgo(30), daysAgo(1))], 'pass'],
  ] as const)('%s -> %s', async (_name, nodes, status) => {
    const outcome = await checkWith([...nodes]).inspect(NOW);
    expect(outcome.status).toBe(status);
    expectContract(outcome);
  });

  it('names the nodes, with the LAI-CLD-031 code and fix', async () => {
    const outcome = await checkWith([node('ai-n5', 'pending', daysAgo(9)), node('ai-n4', 'pending', daysAgo(20)), node('ai-n3', 'enrolled', daysAgo(40))]).inspect(NOW);
    expect(outcome.status).toBe('warn');
    expect(outcome.detail).toContain('LAI-CLD-031');
    expect(outcome.detail).toContain('Retire or finish these ai-n4, ai-n5');
    expect(outcome.detail).not.toContain('ai-n3');
    expect(outcome.remedy).toBe('Finish the install on that computer, or retire the node in the cloud');
    expect(outcome.data).toEqual({ count: 2, oldestDays: 20, code: 'LAI-CLD-031' });
  });

  it('says "Retire or finish ai-n4" for one node', async () => {
    expect((await checkWith([node('ai-n4', 'pending', daysAgo(8))]).inspect(NOW)).detail).toContain('Retire or finish ai-n4');
  });

  it('caps a long list', async () => {
    const many = Array.from({ length: 8 }, (_, index) => node(`ai-n${index + 1}`, 'pending', daysAgo(10 + index)));
    expect((await checkWith(many).inspect(NOW)).detail).toContain('and 3 more');
  });

  it('scrubs a node id that looks like a secret', async () => {
    const bait = leakBait();
    const outcome = await checkWith([node(bait.token, 'pending', daysAgo(9))]).inspect(NOW);
    expect(JSON.stringify(outcome)).not.toContain(bait.token);
  });

  it('run() uses the clock', async () => {
    const check = checkWith([node('ai-n4', 'pending', new Date(Date.now() - 8 * 86_400_000))]);
    expect((await check.run()).status).toBe('warn');
  });
});

describeDoctorCheckConformance('localai.nodes.pending', ({ prisma }) => new NodesPendingDoctorCheck(registry as never, prisma.client), {
  scenarios: [
    { name: 'none waiting', prisma: { results: { 'localaiNode.findMany': [] } }, expectStatus: 'pass' },
    {
      name: 'one waiting',
      prisma: { results: { 'localaiNode.findMany': [{ nodeId: 'ai-n4', createdAt: new Date('2026-01-01T00:00:00Z') }] } },
      expectStatus: 'warn',
    },
  ],
});
