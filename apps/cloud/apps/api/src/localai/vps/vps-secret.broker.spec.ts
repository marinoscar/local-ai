import { VpsSecretBroker, VpsSecretRefusedError, nodeIdOf } from './vps-secret.broker';
import { fakeKey, fakeTailnet, jobRow } from '../../../test/localai/support/vps-fakes';
import { JOB_VPS_PUBLISH, JOB_VPS_UNPUBLISH } from './vps.constants';

function setup(over: { node?: Record<string, unknown> | null; key?: ReturnType<typeof fakeKey> | null; suffix?: string; suffixValid?: boolean } = {}) {
  const key = over.key === undefined ? fakeKey() : over.key;
  const tailnet = fakeTailnet();
  const prisma = {
    localaiNode: {
      findUnique: jest.fn(async () => (over.node === undefined ? { phase: 'enrolled', servePattern: 'B', ...tailnet } : over.node)),
    },
  };
  const keys = { status: jest.fn(async () => ({ configured: key !== null })), read: jest.fn(async () => key) };
  const settings = { read: jest.fn(async () => ({ stagingSuffix: over.suffix ?? '-staging', stagingSuffixValid: over.suffixValid ?? true, vpsPublicHostname: 'ai.marin.cr' })) };
  return { broker: new VpsSecretBroker(prisma as never, keys as never, settings as never), prisma, keys, key, tailnet };
}

const until = new Date('2026-11-04T12:15:00Z');

describe('VpsSecretBroker', () => {
  it('declares its kind and is usable only with a stored key', async () => {
    expect(setup().broker.kind).toBe('localai.vps.route53');
    expect(await setup().broker.usable()).toEqual({ ok: true });
    const none = await setup({ key: null }).broker.usable();
    expect(none).toMatchObject({ ok: false });
    expect(JSON.stringify(none)).not.toMatch(/AKIA/);
  });

  it('issues the publish material: key, zone, host, suffix and the node’s tailnet values', async () => {
    const { broker, key, tailnet } = setup();
    const issued = await broker.issue(jobRow(JOB_VPS_PUBLISH), until);
    expect(issued.material).toEqual({
      route53AccessKeyId: key!.accessKeyId,
      route53SecretAccessKey: key!.secretAccessKey,
      hostedZoneId: key!.hostedZoneId,
      host: 'ai-n3-staging.marin.cr',
      stagingSuffix: '-staging',
      vpsPublicHostname: 'ai.marin.cr',
      servePattern: 'B',
      tailnetIp: tailnet.tailnetIp,
      tailnetMachine: tailnetMachineOf(tailnet),
    });
    expect(issued.expiresAt).toEqual(until);
  });

  it('the handle is the only thing meant to be stored, and holds no secret', async () => {
    const { broker, key, tailnet } = setup();
    const issued = await broker.issue(jobRow(JOB_VPS_PUBLISH), until);
    expect(issued.handle).toBe('vps:job-1');
    for (const secret of [key!.accessKeyId, key!.secretAccessKey, key!.hostedZoneId, tailnet.tailnetIp, tailnet.tailnetMachine]) expect(issued.handle).not.toContain(secret);
    // idempotent per job: asking again gives the same handle
    expect((await broker.issue(jobRow(JOB_VPS_PUBLISH), until)).handle).toBe(issued.handle);
  });

  it('issues unpublish material without any tailnet field and without reading the node', async () => {
    const { broker, prisma } = setup({ node: null });
    const issued = await broker.issue(jobRow(JOB_VPS_UNPUBLISH), until);
    expect(Object.keys(issued.material).sort()).toEqual(['host', 'hostedZoneId', 'route53AccessKeyId', 'route53SecretAccessKey', 'stagingSuffix', 'vpsPublicHostname']);
    expect(prisma.localaiNode.findUnique).not.toHaveBeenCalled();
  });

  it('uses the empty suffix in production', async () => {
    expect((await setup({ suffix: '' }).broker.issue(jobRow(JOB_VPS_UNPUBLISH), until)).material.host).toBe('ai-n3.marin.cr');
  });

  it.each([
    ['no key', { key: null }, /No Route53 key/],
    ['an invalid suffix', { suffixValid: false }, /stagingSuffix/],
    ['a missing node', { node: null }, /does not exist/],
    ['a retired node', { node: { phase: 'retired', servePattern: 'B', tailnetIp: 'x', tailnetMachine: 'y' } }, /retired/],
    ['no serve pattern', { node: { phase: 'enrolled', servePattern: 'none', tailnetIp: 'x', tailnetMachine: 'y' } }, /serve pattern/],
    ['no tailnet address', { node: { phase: 'enrolled', servePattern: 'B', tailnetIp: null, tailnetMachine: 'y' } }, /tailnet/],
  ])('refuses a publish for %s with a fixed message', async (_label, over, pattern) => {
    const { broker } = setup(over as never);
    const error = (await broker.issue(jobRow(JOB_VPS_PUBLISH), until).catch((e: unknown) => e)) as Error;
    expect(error).toBeInstanceOf(VpsSecretRefusedError);
    expect(error.message).toMatch(pattern);
  });

  it('refuses a job that names no lab node', async () => {
    const { broker } = setup();
    await expect(broker.issue({ id: 'j', type: JOB_VPS_PUBLISH, payload: { nodeId: '../etc' }, subjectId: null } as never, until)).rejects.toBeInstanceOf(VpsSecretRefusedError);
    expect(() => nodeIdOf({ payload: null, subjectId: 'ai-n7' } as never)).not.toThrow();
  });

  it('revoke tolerates any handle', async () => {
    await expect(setup().broker.revoke()).resolves.toBeUndefined();
  });
});

function tailnetMachineOf(t: { tailnetMachine: string }) {
  return t.tailnetMachine;
}
