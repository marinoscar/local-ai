// The node's own tailnet report (issue #437): what is stored, that a repeat changes nothing, that the address reaches
// no audit row, no log line and no answer, and that the contract refuses what is not a Tailscale address.
import { Logger } from '@nestjs/common';
import type { LocalaiNode } from '@prisma/client';

import { NodeTailnetRequestSchema, tailnetIssueFields } from '../contracts/tailnet.schema';
import { NodeTailnetService } from './tailnet.service';

// Addresses in the Tailscale block, built at run time so no address-shaped literal sits in the repository.
const tailnetIp = (last: number) => ['100', '64', '10', String(last)].join('.');
const IP_A = tailnetIp(20);
const IP_B = tailnetIp(21);

const row = (extra: Partial<LocalaiNode> = {}): LocalaiNode => ({ id: 'row-1', nodeId: 'ai-n4', tailnetIp: null, tailnetMachine: null, ...extra }) as LocalaiNode;

function rig() {
  const updates: Array<Record<string, unknown>> = [];
  const audits: Array<Record<string, unknown>> = [];
  const prisma = { localaiNode: { update: async ({ data }: { data: Record<string, unknown> }) => void updates.push(data) } };
  const audit = { record: async (event: Record<string, unknown>) => void audits.push(event) };
  const logs: string[] = [];
  for (const level of ['log', 'warn', 'error', 'debug', 'verbose'] as const) jest.spyOn(Logger.prototype, level).mockImplementation((...args: unknown[]) => void logs.push(String(args[0])));
  return { service: new NodeTailnetService(prisma as never, audit as never), updates, audits, logs };
}

afterEach(() => jest.restoreAllMocks());

describe('NodeTailnetService', () => {
  it('stores the address and the name of a node that has none, and audits the words only', async () => {
    const { service, updates, audits, logs } = rig();
    const answer = await service.report(row(), { ip: IP_A, machine: 'dell-linux.example.ts.net' }, 'user-1');
    expect(answer).toEqual({ stored: true });
    expect(updates).toEqual([{ tailnetIp: IP_A, tailnetMachine: 'dell-linux.example.ts.net' }]);
    expect(audits).toHaveLength(1);
    expect(audits[0]).toMatchObject({ action: 'localai.node.updated', targetId: 'ai-n4', meta: { tailnetIp: '<set>', tailnetMachine: 'dell-linux.example.ts.net', source: 'node' } });
    expect(JSON.stringify([audits, answer, logs])).not.toContain(IP_A);
  });

  it('the same report again changes nothing and writes no audit row', async () => {
    const { service, updates, audits } = rig();
    const answer = await service.report(row({ tailnetIp: IP_A, tailnetMachine: 'dell-linux' }), { ip: IP_A, machine: 'dell-linux' }, 'user-1');
    expect(answer).toEqual({ stored: false });
    expect(updates).toEqual([]);
    expect(audits).toEqual([]);
  });

  it('a new address replaces the stored one and is audited as changed, not as a value', async () => {
    const { service, updates, audits } = rig();
    await service.report(row({ tailnetIp: IP_A }), { ip: IP_B }, null);
    expect(updates).toEqual([{ tailnetIp: IP_B }]);
    expect(audits[0]).toMatchObject({ meta: { tailnetIp: '<changed>', source: 'node' } });
    expect(JSON.stringify(audits)).not.toContain(IP_B);
  });

  it('keeps the stored name when the report has none', async () => {
    const { service, updates } = rig();
    await service.report(row({ tailnetMachine: 'dell-linux' }), { ip: IP_A }, null);
    expect(updates).toEqual([{ tailnetIp: IP_A }]);
  });
});

describe('NodeTailnetRequestSchema', () => {
  it.each([
    [{ ip: IP_A }],
    [{ ip: IP_A, machine: 'Dell-Linux.Example.ts.net' }],
    [{ ip: 'fd7a:115c:a1e0:0:0:0:1' }],
  ])('accepts %j', (body) => expect(NodeTailnetRequestSchema.safeParse(body).success).toBe(true));

  it.each([
    [{ ip: '192.0.2.10' }, 'a LAN-looking address'],
    [{ ip: '100.200.1.1' }, 'outside the CGNAT block'],
    [{ ip: IP_A, machine: 'not a name' }, 'a machine name with spaces'],
    [{ ip: IP_A, extra: 1 }, 'an unknown field'],
    [{}, 'no address'],
  ])('refuses %j (%s) and the field list has no value', (body: unknown, _why: string) => {
    const parsed = NodeTailnetRequestSchema.safeParse(body);
    expect(parsed.success).toBe(false);
    if (!parsed.success) expect(JSON.stringify(tailnetIssueFields(parsed.error))).not.toMatch(/\d+\.\d+\.\d+\.\d+/);
  });

  it('lower-cases the machine name', () => {
    expect(NodeTailnetRequestSchema.parse({ ip: IP_A, machine: 'Dell-Linux' }).machine).toBe('dell-linux');
  });
});
