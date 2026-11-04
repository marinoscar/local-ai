import { describeDoctorCheckConformance } from '@marinoscar/platform-api/doctor/testing';

import { fakePrisma } from '../../../test/localai/support/fake-tables';
import { expectContract } from '../../../test/localai/support/doctor-asserts';
import { NodesKeysDoctorCheck } from './nodes-keys.check';

const registry = { register: jest.fn() };
const NOW = new Date('2026-11-10T12:00:00Z');
const minutesAgo = (minutes: number) => new Date(NOW.getTime() - minutes * 60_000);
const node = (nodeId: string, over: Record<string, unknown> = {}) => ({ nodeId, phase: 'enrolled', retiredAt: null, keyReportedAt: null, createdAt: minutesAgo(60 * 24), ...over });
const enrolledEvent = (targetId: string, createdAt: Date) => ({ action: 'localai.node.enrolled', targetType: 'localai_node', targetId, createdAt });
const checkWith = (nodes: Array<Record<string, unknown>>, audit: Array<Record<string, unknown>> = []) =>
  new NodesKeysDoctorCheck(registry as never, fakePrisma({ localaiNode: nodes, auditEvent: audit }) as never);

describe('localai.nodes.keys', () => {
  it('registers itself', () => {
    const check = checkWith([]);
    check.onModuleInit();
    expect(registry.register).toHaveBeenCalledWith(check);
  });

  it.each([
    ['no nodes', [], [], 'pass'],
    ['every enrolled node has reported its key', [node('ai-n1', { keyReportedAt: minutesAgo(5) })], [], 'pass'],
    ['enrolled 9 minutes ago, no key yet', [node('ai-n1')], [enrolledEvent('ai-n1', minutesAgo(9))], 'pass'],
    ['enrolled 11 minutes ago, no key', [node('ai-n1')], [enrolledEvent('ai-n1', minutesAgo(11))], 'warn'],
    ['pending nodes are not enrolled', [node('ai-n1', { phase: 'pending' })], [], 'pass'],
    ['retired nodes have no key on purpose', [node('ai-n1', { phase: 'retired', retiredAt: minutesAgo(1) })], [], 'pass'],
    ['no audit row: timed from the record, old', [node('ai-n1', { createdAt: minutesAgo(120) })], [], 'warn'],
    ['no audit row: timed from the record, young', [node('ai-n1', { createdAt: minutesAgo(3) })], [], 'pass'],
    ['an old record re-enrolled 2 minutes ago uses the latest enrollment', [node('ai-n1')], [enrolledEvent('ai-n1', minutesAgo(600)), enrolledEvent('ai-n1', minutesAgo(2))], 'pass'],
  ] as const)('%s -> %s', async (_name, nodes, audit, status) => {
    const outcome = await checkWith([...nodes], [...audit]).inspect(NOW);
    expect(outcome.status).toBe(status);
    expectContract(outcome);
  });

  it('names the nodes with the LAI-CLD-026 code and fix, and ignores nodes still inside the grace period', async () => {
    const outcome = await checkWith(
      [node('ai-n2'), node('ai-n1'), node('ai-n3')],
      [enrolledEvent('ai-n1', minutesAgo(30)), enrolledEvent('ai-n2', minutesAgo(45)), enrolledEvent('ai-n3', minutesAgo(1))],
    ).inspect(NOW);
    expect(outcome.status).toBe('warn');
    expect(outcome.detail).toContain('LAI-CLD-026');
    expect(outcome.detail).toContain('no key stored for ai-n1, ai-n2');
    expect(outcome.detail).not.toContain('ai-n3');
    expect(outcome.remedy).toContain('sudo aictl cloud resend-key');
    expect(outcome.data).toEqual({ count: 2, code: 'LAI-CLD-026' });
  });
});

describeDoctorCheckConformance('localai.nodes.keys', ({ prisma }) => new NodesKeysDoctorCheck(registry as never, prisma.client), {
  scenarios: [
    { name: 'no keyless node', prisma: { results: { 'localaiNode.findMany': [] } }, expectStatus: 'pass' },
    {
      name: 'a keyless node',
      prisma: {
        results: {
          'localaiNode.findMany': [{ nodeId: 'ai-n1', createdAt: new Date('2026-01-01T00:00:00Z') }],
          'auditEvent.findMany': [],
        },
      },
      expectStatus: 'warn',
    },
  ],
});
