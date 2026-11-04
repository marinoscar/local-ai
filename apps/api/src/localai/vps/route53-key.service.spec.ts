import { Route53KeyBodySchema, Route53KeyService, Route53KeyUnreadableError } from './route53-key.service';
import { fakeKey, FakeCredentials } from '../../../test/localai/support/vps-fakes';

describe('Route53KeyService', () => {
  let credentials: FakeCredentials;
  let audit: { record: jest.Mock };
  let service: Route53KeyService;
  beforeEach(() => {
    credentials = new FakeCredentials();
    audit = { record: jest.fn(async () => undefined) };
    service = new Route53KeyService(credentials as never, audit as never);
  });

  it('is not configured until all three entries are stored', async () => {
    expect((await service.status()).configured).toBe(false);
    await credentials.setSecret('localai_route53', 'access_key_id', fakeKey().accessKeyId);
    expect((await service.status()).configured).toBe(false);
    expect(await service.read()).toBeNull();
  });

  it('stores the key, answers with masked hints only, and audits the hint', async () => {
    const key = fakeKey();
    const status = await service.set(key, 'user-1');
    expect(status.configured).toBe(true);
    expect(status.accessKeyHint).toBe(`****${key.accessKeyId.slice(-4)}`);
    const shown = JSON.stringify(status) + JSON.stringify(audit.record.mock.calls);
    for (const secret of [key.accessKeyId, key.secretAccessKey, key.hostedZoneId]) expect(shown).not.toContain(secret);
    expect(audit.record).toHaveBeenCalledWith(expect.objectContaining({ action: 'localai.vps.route53_key_set', actorUserId: 'user-1' }));
    expect(await service.read()).toEqual(key);
  });

  it('writes the secret half first, so a half-written key never reads as configured', async () => {
    const order: string[] = [];
    credentials.setSecret = async (_p: string, name: string) => {
      order.push(name);
    };
    await service.set(fakeKey(), null).catch(() => undefined);
    expect(order).toEqual(['secret_access_key', 'access_key_id', 'hosted_zone_id']);
  });

  it('clear removes everything and is idempotent', async () => {
    await service.set(fakeKey(), null);
    expect((await service.clear('user-1')).configured).toBe(false);
    expect((await service.clear('user-1')).configured).toBe(false);
    expect(audit.record).toHaveBeenCalledWith(expect.objectContaining({ action: 'localai.vps.route53_key_cleared' }));
  });

  it('an unreadable key is a named error that carries no cause', async () => {
    const key = fakeKey();
    await service.set(key, null);
    credentials.failReads = true;
    const error = (await service.read().catch((e: unknown) => e)) as Error;
    expect(error).toBeInstanceOf(Route53KeyUnreadableError);
    expect(error.message).not.toContain(key.secretAccessKey);
  });

  it('a failed audit write does not undo the change', async () => {
    audit.record.mockRejectedValue(new Error('db down'));
    await expect(service.set(fakeKey(), null)).resolves.toMatchObject({ configured: true });
  });
});

describe('Route53KeyBodySchema', () => {
  it('accepts a well-formed key and refuses the rest without echoing it', () => {
    expect(Route53KeyBodySchema.safeParse(fakeKey()).success).toBe(true);
    for (const bad of [{ ...fakeKey(), accessKeyId: 'nope' }, { ...fakeKey(), secretAccessKey: 'short' }, { ...fakeKey(), hostedZoneId: 'zone' }, { ...fakeKey(), extra: 1 }]) {
      const parsed = Route53KeyBodySchema.safeParse(bad);
      expect(parsed.success).toBe(false);
      expect(JSON.stringify(parsed.error?.issues)).not.toContain(String((bad as { secretAccessKey: string }).secretAccessKey));
    }
  });
});
