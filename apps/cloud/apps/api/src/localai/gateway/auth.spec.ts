import { GatewayAuthenticator, GATEWAY_USE_PERMISSION, LAST_USED_INTERVAL_MS, clientIp, type AuthenticateInput } from './auth';
import { FailedAuthLimiter } from './failed-auth-limiter';
import { ACTIVE_OWNER, FakeKeyStore, FakeOwners, makeKey, makeToken } from './gateway.testkit';
import { GatewayError } from './errors';
import { hashGatewayToken } from './token-verify';

const IP = '203.0.113.9';

describe('GatewayAuthenticator', () => {
  let now: number;
  let store: FakeKeyStore;
  let owners: FakeOwners;
  let token: string;
  let auth: GatewayAuthenticator;

  const attempt = (authorization: string | undefined, ip = IP): Promise<unknown> => auth.authenticate({ authorization, ip } satisfies AuthenticateInput);
  const refusal = async (authorization: string | undefined, ip = IP): Promise<GatewayError> => {
    try {
      await attempt(authorization, ip);
    } catch (error) {
      return error as GatewayError;
    }
    throw new Error('expected a refusal');
  };

  beforeEach(() => {
    now = Date.parse('2026-11-04T12:00:00Z');
    store = new FakeKeyStore();
    owners = new FakeOwners();
    owners.set('user-a', ACTIVE_OWNER);
    token = makeToken();
    store.add(makeKey(token, { expiresAt: new Date(now + 86_400_000) }));
    auth = new GatewayAuthenticator({ store, owners, now: () => now });
  });

  it('accepts a valid key and returns the key and its owner', async () => {
    const principal = (await attempt(`Bearer ${token}`)) as { key: { tokenPrefix: string }; userId: string };
    expect(principal.userId).toBe('user-a');
    expect(principal.key.tokenPrefix).toBe(token.slice(0, 10));
  });

  describe('refusals (401 LAI-GW-010)', () => {
    it.each<[string, () => string | undefined]>([
      ['no Authorization header', () => undefined],
      ['an empty header', () => ''],
      ['a Basic header', () => `Basic ${Buffer.from('a:b').toString('base64')}`],
      ['a bare token without Bearer', () => token],
      ['a token that is too short', () => `Bearer ${token.slice(0, 40)}`],
      ['a token that is too long', () => `Bearer ${token}x`],
      ['a token with a forbidden character', () => `Bearer ${token.slice(0, 42)}!`],
      ['a wrong prefix', () => `Bearer sk_${token.slice(4)}`],
      ['a JWT-shaped token', () => 'Bearer aaaa.bbbb.cccc'],
    ])('%s makes ZERO database queries', async (_label, header) => {
      const err = await refusal(header());
      expect(err).toBeInstanceOf(GatewayError);
      expect(err.localaiCode).toBe('LAI-GW-010');
      expect(err.status).toBe(401);
      expect(store.lookups).toBe(0);
      expect(owners.reads).toBe(0);
    });

    it('an unknown (well-formed) key looks the hash up once and is refused', async () => {
      const err = await refusal(`Bearer ${makeToken()}`);
      expect(err.localaiCode).toBe('LAI-GW-010');
      expect(store.lookups).toBe(1);
    });

    it('a revoked key is refused', async () => {
      const revoked = makeToken();
      store.add(makeKey(revoked, { revokedAt: new Date(now - 1000) }));
      expect((await refusal(`Bearer ${revoked}`)).localaiCode).toBe('LAI-GW-010');
    });

    it('an expired key is refused (expiresAt <= now), a key expiring later is not', async () => {
      const expired = makeToken();
      store.add(makeKey(expired, { expiresAt: new Date(now) }));
      expect((await refusal(`Bearer ${expired}`)).localaiCode).toBe('LAI-GW-010');
      const later = makeToken();
      store.add(makeKey(later, { expiresAt: new Date(now + 1) }));
      await expect(attempt(`Bearer ${later}`)).resolves.toBeDefined();
    });

    it('a service key with expiresAt null is valid', async () => {
      const service = makeToken();
      store.add(makeKey(service, { kind: 'service', expiresAt: null, dailyTokenBudget: null }));
      await expect(attempt(`Bearer ${service}`)).resolves.toBeDefined();
    });

    it('accepts the Bearer scheme in any case and trailing spaces', async () => {
      await expect(attempt(`bearer ${token} `)).resolves.toBeDefined();
    });
  });

  describe('the owner (403 LAI-GW-013)', () => {
    it('a deactivated owner is refused', async () => {
      owners.set('user-a', { active: false, permissions: [GATEWAY_USE_PERMISSION] });
      const err = await refusal(`Bearer ${token}`);
      expect(err.localaiCode).toBe('LAI-GW-013');
      expect(err.status).toBe(403);
    });

    it('an owner who lost gateway:use is refused', async () => {
      owners.set('user-a', { active: true, permissions: ['lab:read'] });
      expect((await refusal(`Bearer ${token}`)).localaiCode).toBe('LAI-GW-013');
    });

    it('a deleted owner is refused', async () => {
      owners.set('user-a', null);
      expect((await refusal(`Bearer ${token}`)).localaiCode).toBe('LAI-GW-013');
    });

    it('the owner answer is cached for 60 s, then read again', async () => {
      await attempt(`Bearer ${token}`);
      owners.set('user-a', { active: false, permissions: [] });
      now += 59_000;
      await expect(attempt(`Bearer ${token}`)).resolves.toBeDefined();
      expect(owners.reads).toBe(1);
      now += 2_000;
      expect((await refusal(`Bearer ${token}`)).localaiCode).toBe('LAI-GW-013');
      expect(owners.reads).toBe(2);
    });

    it('is not a failed attempt of the address', async () => {
      owners.set('user-a', { active: false, permissions: [] });
      for (let i = 0; i < 40; i += 1) expect((await refusal(`Bearer ${token}`)).localaiCode).toBe('LAI-GW-013');
    });
  });

  describe('the 10 s positive cache', () => {
    it('serves a repeat from memory and looks the hash up again after 10 s', async () => {
      await attempt(`Bearer ${token}`);
      await attempt(`Bearer ${token}`);
      expect(store.lookups).toBe(1);
      now += 10_001;
      await attempt(`Bearer ${token}`);
      expect(store.lookups).toBe(2);
    });

    it('a key revoked after it was cached is refused within 10 s (worst case)', async () => {
      await attempt(`Bearer ${token}`);
      store.keys.get(hashGatewayToken(token))!.revokedAt = new Date(now);
      await attempt(`Bearer ${token}`); // still cached
      now += 10_001;
      expect((await refusal(`Bearer ${token}`)).localaiCode).toBe('LAI-GW-010');
    });

    it('evict(tokenHash) makes the very next request refuse a revoked key', async () => {
      await attempt(`Bearer ${token}`);
      store.keys.get(hashGatewayToken(token))!.revokedAt = new Date(now);
      auth.evict(hashGatewayToken(token));
      expect((await refusal(`Bearer ${token}`)).localaiCode).toBe('LAI-GW-010');
    });

    it('does not cache negative results: a key created after a miss works at once', async () => {
      const late = makeToken();
      expect((await refusal(`Bearer ${late}`)).localaiCode).toBe('LAI-GW-010');
      store.add(makeKey(late));
      await expect(attempt(`Bearer ${late}`)).resolves.toBeDefined();
    });

    it('a cached key that has since expired is refused', async () => {
      const soon = makeToken();
      store.add(makeKey(soon, { expiresAt: new Date(now + 5_000) }));
      await attempt(`Bearer ${soon}`);
      now += 6_000;
      expect((await refusal(`Bearer ${soon}`)).localaiCode).toBe('LAI-GW-010');
    });
  });

  describe('the failed-auth limiter', () => {
    it('30 wrong keys from one address: the 31st attempt is 429 LAI-GW-011 with Retry-After, even with a valid key', async () => {
      for (let i = 0; i < 30; i += 1) expect((await refusal('Bearer nope')).localaiCode).toBe('LAI-GW-010');
      const blocked = await refusal(`Bearer ${token}`);
      expect(blocked.localaiCode).toBe('LAI-GW-011');
      expect(blocked.status).toBe(429);
      expect(blocked.retryAfterS).toBe(300);
      // another address is unaffected
      await expect(attempt(`Bearer ${token}`, '203.0.113.10')).resolves.toBeDefined();
    });

    it('is lifted after 5 minutes', async () => {
      for (let i = 0; i < 30; i += 1) await refusal(undefined);
      expect((await refusal(`Bearer ${token}`)).localaiCode).toBe('LAI-GW-011');
      now += 5 * 60_000;
      await expect(attempt(`Bearer ${token}`)).resolves.toBeDefined();
    });

    it('revoked and unknown keys count as failures, a valid key does not', async () => {
      const limiter = new FailedAuthLimiter(() => now, 3);
      auth = new GatewayAuthenticator({ store, owners, now: () => now, limiter });
      await attempt(`Bearer ${token}`);
      await attempt(`Bearer ${token}`);
      await refusal(`Bearer ${makeToken()}`);
      await refusal(`Bearer ${makeToken()}`);
      await attempt(`Bearer ${token}`);
      await refusal(undefined);
      expect((await refusal(`Bearer ${token}`)).localaiCode).toBe('LAI-GW-011');
    });
  });

  describe('lastUsedAt', () => {
    it('is written fire-and-forget at most once a minute per key', async () => {
      await attempt(`Bearer ${token}`);
      await attempt(`Bearer ${token}`);
      expect(store.touches).toHaveLength(1);
      now += LAST_USED_INTERVAL_MS - 1;
      await attempt(`Bearer ${token}`);
      expect(store.touches).toHaveLength(1);
      now += 1;
      await attempt(`Bearer ${token}`);
      expect(store.touches).toHaveLength(2);
      expect(store.touches[1]!.at.getTime()).toBe(now);
    });

    it('a failing write never fails the request and reports to the hook', async () => {
      const errors: unknown[] = [];
      store.touchError = new Error('db down');
      auth = new GatewayAuthenticator({ store, owners, now: () => now, onTouchError: (e) => errors.push(e) });
      await expect(attempt(`Bearer ${token}`)).resolves.toBeDefined();
      await new Promise((resolve) => setImmediate(resolve));
      expect(errors).toHaveLength(1);
    });
  });
});

describe('clientIp', () => {
  it('prefers X-Real-IP, falls back to the socket address, ignores junk', () => {
    expect(clientIp({ 'x-real-ip': '203.0.113.5' }, '10.0.0.1')).toBe('203.0.113.5');
    expect(clientIp({ 'x-real-ip': '2001:db8::1' }, '10.0.0.1')).toBe('2001:db8::1');
    expect(clientIp({ 'x-real-ip': 'not an ip; drop table' }, '10.0.0.1')).toBe('10.0.0.1');
    expect(clientIp({}, undefined)).toBe('unknown');
  });
});
