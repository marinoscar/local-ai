import { BadRequestException, ConflictException, ForbiddenException, NotFoundException } from '@nestjs/common';

import { AUDIT_GATEWAY_KEY_CREATED, AUDIT_GATEWAY_KEY_REVOKED, AUDIT_GATEWAY_KEY_UPDATED, AUDIT_TARGET_GATEWAY_KEY } from '../gateway.audit';
import { GATEWAY_TOKEN_PATTERN, hashGatewayToken } from '../gateway.token';
import { FakeKeyStore } from './fake-key.store';
import type { KeyActor } from './key-rules';
import { KeyService } from './key.service';
import type { GatewayKeyDefaults } from '../../settings.service';

const DEFAULTS: GatewayKeyDefaults = {
  user: { rpm: 60, concurrent: 4, dailyTokens: 2_000_000, expiresInDays: 90 },
  service: { rpm: 600, concurrent: 16, dailyTokens: null, expiresInDays: null },
  maxUserKeyDays: 365,
};
const NOW = new Date('2026-11-04T12:00:00.000Z');
const USER_A = '11111111-1111-4111-8111-111111111111';
const USER_B = '22222222-2222-4222-8222-222222222222';
const ADMIN = '33333333-3333-4333-8333-333333333333';

const userA: KeyActor = { userId: USER_A, permissions: ['gateway:use'] };
const userB: KeyActor = { userId: USER_B, permissions: ['gateway:use'] };
const admin: KeyActor = { userId: ADMIN, permissions: ['gateway:use', 'gateway:admin'] };

function build(overrides: { audit?: { record: jest.Mock }; cache?: { evict: jest.Mock } | null; known?: string[]; defaults?: GatewayKeyDefaults } = {}) {
  const store = new FakeKeyStore();
  store.users.add(USER_A).add(USER_B).add(ADMIN);
  const audit = overrides.audit ?? { record: jest.fn().mockResolvedValue(undefined) };
  const cache = overrides.cache === undefined ? { evict: jest.fn() } : overrides.cache;
  const known = new Set(overrides.known ?? ['qwen3.5-9b-q4km', 'qwen3-coder-next-q4km', 'lab-coder']);
  const service = new KeyService(
    store,
    { read: async () => overrides.defaults ?? DEFAULTS },
    { names: async () => known },
    audit as never,
    cache,
    { now: () => NOW },
  );
  return { service, store, audit, cache };
}

describe('KeyService.create', () => {
  it('returns the token once, in the format of the spec, and stores only its hash and prefix', async () => {
    const { service, store } = build();
    const created = await service.create(userA, { name: 'OpenCode laptop' });
    expect(created.token).toMatch(/^lai_[A-Za-z0-9_-]{43}$/);
    expect(created.token).toMatch(GATEWAY_TOKEN_PATTERN);
    const row = store.rows[0]!;
    expect(row.tokenHash).toBe(hashGatewayToken(created.token));
    expect(row.tokenPrefix).toBe(created.token.slice(0, 10));
    expect(row.tokenPrefix).toHaveLength(10);
    expect(JSON.stringify(store.rows, (_k, v) => (typeof v === 'bigint' ? v.toString() : v))).not.toContain(created.token);
    expect(created.key.tokenPrefix).toBe(row.tokenPrefix);
  });

  it('the key it returns has no token and no hash', async () => {
    const { service, store } = build();
    const created = await service.create(userA, { name: 'k' });
    const text = JSON.stringify(created.key);
    expect(text).not.toContain(created.token);
    expect(text).not.toContain(store.rows[0]!.tokenHash);
    expect(Object.keys(created.key)).not.toContain('tokenHash');
    expect(Object.keys(created.key)).not.toContain('token');
  });

  it('stores the limits and expiry from the settings', async () => {
    const { service } = build();
    const { key } = await service.create(userA, { name: 'k' });
    expect(key).toMatchObject({
      kind: 'user',
      ownerUserId: USER_A,
      limits: { rpm: 60, concurrent: 4, dailyTokens: 2_000_000 },
      expiresAt: '2027-02-02T12:00:00.000Z',
      status: 'active',
      allowedModels: [],
    });
  });

  it('changing the settings changes the next key', async () => {
    const first = build();
    const second = build({ defaults: { ...DEFAULTS, user: { rpm: 5, concurrent: 1, dailyTokens: 1000, expiresInDays: 7 } } });
    expect((await first.service.create(userA, { name: 'k' })).key.limits).toEqual({ rpm: 60, concurrent: 4, dailyTokens: 2_000_000 });
    const changed = (await second.service.create(userA, { name: 'k' })).key;
    expect(changed.limits).toEqual({ rpm: 5, concurrent: 1, dailyTokens: 1000 });
    expect(changed.expiresAt).toBe('2026-11-11T12:00:00.000Z');
  });

  it('two keys never share a token', async () => {
    const { service } = build();
    const a = await service.create(userA, { name: 'a' });
    const b = await service.create(userA, { name: 'b' });
    expect(a.token).not.toBe(b.token);
  });

  it('refuses a model the lab does not know (400, naming it) and stores nothing', async () => {
    const { service, store } = build();
    await expect(service.create(userA, { name: 'k', allowedModels: ['qwen3.5-9b-q4km', 'no-such-model'] })).rejects.toThrow(BadRequestException);
    await expect(service.create(userA, { name: 'k', allowedModels: ['no-such-model'] })).rejects.toThrow(/no-such-model/);
    expect(store.rows).toHaveLength(0);
  });

  it('accepts catalog ids and aliases', async () => {
    const { service } = build();
    const { key } = await service.create(userA, { name: 'k', allowedModels: ['lab-coder', 'qwen3.5-9b-q4km'] });
    expect(key.allowedModels).toEqual(['lab-coder', 'qwen3.5-9b-q4km']);
  });

  it('refuses an owner that does not exist (400)', async () => {
    const { service } = build();
    await expect(service.create(admin, { name: 'k', ownerUserId: '44444444-4444-4444-8444-444444444444' })).rejects.toThrow(BadRequestException);
  });

  it('a service key: admin only, owner and creator recorded', async () => {
    const { service, store } = build();
    await expect(service.create(userA, { name: 'open-webui', kind: 'service' })).rejects.toThrow(ForbiddenException);
    const { key } = await service.create(admin, { name: 'open-webui', kind: 'service' });
    expect(key).toMatchObject({ kind: 'service', expiresAt: null, limits: { rpm: 600, concurrent: 16, dailyTokens: null } });
    expect(store.rows[0]).toMatchObject({ userId: ADMIN, createdById: ADMIN });
  });

  it('an administrator issuing a user key for someone else records who issued it', async () => {
    const { service, store } = build();
    const { key } = await service.create(admin, { name: 'for B', ownerUserId: USER_B });
    expect(key.ownerUserId).toBe(USER_B);
    expect(store.rows[0]).toMatchObject({ userId: USER_B, createdById: ADMIN });
  });

  it('writes one key_created audit row with prefix, name and limits and never the token or hash', async () => {
    const { service, audit, store } = build();
    const created = await service.create(userA, { name: 'OpenCode laptop', allowedModels: ['lab-coder'] });
    expect(audit.record).toHaveBeenCalledTimes(1);
    const row = audit.record.mock.calls[0]![0];
    expect(row).toMatchObject({
      action: AUDIT_GATEWAY_KEY_CREATED,
      actorUserId: USER_A,
      targetType: AUDIT_TARGET_GATEWAY_KEY,
      targetId: store.rows[0]!.id,
      meta: { prefix: created.token.slice(0, 10), name: 'OpenCode laptop', rpm: 60, concurrent: 4, dailyTokens: 2_000_000, allowedModels: 'lab-coder', kind: 'user' },
    });
    const text = JSON.stringify(row);
    expect(text).not.toContain(created.token);
    expect(text).not.toContain(created.token.slice(10));
    expect(text).not.toContain(store.rows[0]!.tokenHash);
    for (const value of Object.values(row.meta)) expect(['string', 'number', 'boolean'].includes(typeof value) || value === null).toBe(true);
  });

  it('a failing audit write is logged but does not fail the creation', async () => {
    const { service, store } = build({ audit: { record: jest.fn().mockRejectedValue(new Error('db down')) } });
    await expect(service.create(userA, { name: 'k' })).resolves.toBeDefined();
    expect(store.rows).toHaveLength(1);
  });
});

describe('KeyService.list', () => {
  async function seeded() {
    const ctx = build();
    await ctx.service.create(userA, { name: 'a1' });
    await ctx.service.create(userA, { name: 'a2' });
    await ctx.service.create(userB, { name: 'b1' });
    return ctx;
  }

  it("lists only the caller's keys, newest first", async () => {
    const { service } = await seeded();
    const keys = await service.list(userA, { all: false, includeRevoked: false });
    expect(keys.map((k) => k.name)).toEqual(['a2', 'a1']);
    expect(keys.every((k) => k.ownerUserId === USER_A)).toBe(true);
  });

  it('never shows the owner of a key to its owner', async () => {
    const { service } = await seeded();
    expect((await service.list(userA, { all: false, includeRevoked: false }))[0]!.owner).toBeUndefined();
  });

  it('?all=1 is 403 for a non-administrator', async () => {
    const { service } = await seeded();
    await expect(service.list(userA, { all: true, includeRevoked: false })).rejects.toThrow(ForbiddenException);
  });

  it('?all=1 lists every key, with the owner, for an administrator', async () => {
    const { service } = await seeded();
    const keys = await service.list(admin, { all: true, includeRevoked: false });
    expect(keys.map((k) => k.name)).toEqual(['b1', 'a2', 'a1']);
    expect(keys[0]!.owner).toMatchObject({ email: expect.stringContaining('@example.test') });
  });

  it('an administrator without ?all=1 lists their own keys only', async () => {
    const { service } = await seeded();
    expect(await service.list(admin, { all: false, includeRevoked: false })).toEqual([]);
  });

  it('hides revoked keys unless asked, and shows their status', async () => {
    const { service, store } = await seeded();
    await service.revoke(userA, store.rows[0]!.id);
    expect((await service.list(userA, { all: false, includeRevoked: false })).map((k) => k.name)).toEqual(['a2']);
    const all = await service.list(userA, { all: false, includeRevoked: true });
    expect(all.map((k) => [k.name, k.status])).toEqual([['a2', 'active'], ['a1', 'revoked']]);
  });

  it('shows an expired key as expired', async () => {
    const { service, store } = await seeded();
    store.rows[0]!.expiresAt = new Date(NOW.getTime() - 1000);
    expect((await service.list(userA, { all: false, includeRevoked: false })).find((k) => k.name === 'a1')!.status).toBe('expired');
  });

  it('the list carries no token and no hash for any key', async () => {
    const { service, store } = await seeded();
    const text = JSON.stringify(await service.list(admin, { all: true, includeRevoked: true }));
    for (const row of store.rows) expect(text).not.toContain(row.tokenHash);
    expect(text).not.toMatch(/lai_[A-Za-z0-9_-]{43}/);
    expect(text).not.toContain('tokenHash');
  });
});

describe('KeyService.update', () => {
  it('the owner renames and narrows; the cache entry is evicted; one key_updated row is written', async () => {
    const { service, store, cache, audit } = build();
    const { key } = await service.create(userA, { name: 'old' });
    audit.record.mockClear();
    const updated = await service.update(userA, key.id, { name: 'new', allowedModels: ['lab-coder'] });
    expect(updated).toMatchObject({ name: 'new', allowedModels: ['lab-coder'] });
    expect(cache!.evict).toHaveBeenCalledWith(store.rows[0]!.tokenHash);
    expect(audit.record).toHaveBeenCalledTimes(1);
    expect(audit.record.mock.calls[0]![0]).toMatchObject({ action: AUDIT_GATEWAY_KEY_UPDATED, meta: { name: 'new', changed: 'name,allowedModels', allowedModels: 'lab-coder' } });
  });

  it('narrowing to entries the key already had needs no catalog lookup (a model that left the catalog can be kept)', async () => {
    const { service, store } = build({ known: [] });
    // seed directly: a key allowed two models, one of which is no longer known
    const row = await store.insert({ userId: USER_A, name: 'k', kind: 'user', tokenHash: 'h'.repeat(64), tokenPrefix: 'lai_abcdef', allowedModels: ['gone-model', 'other-model'], rpmLimit: 1, concurrentLimit: 1, dailyTokenBudget: 1n, expiresAt: null, createdById: USER_A });
    const updated = await service.update(userA, row.id, { allowedModels: ['gone-model'] });
    expect(updated.allowedModels).toEqual(['gone-model']);
  });

  it('the owner cannot widen or change limits (403) and nothing changes', async () => {
    const { service, store, cache } = build();
    const { key } = await service.create(userA, { name: 'k', allowedModels: ['lab-coder'] });
    await expect(service.update(userA, key.id, { allowedModels: ['lab-coder', 'qwen3.5-9b-q4km'] })).rejects.toThrow(ForbiddenException);
    await expect(service.update(userA, key.id, { allowedModels: [] })).rejects.toThrow(ForbiddenException);
    await expect(service.update(userA, key.id, { limits: { rpm: 1 } })).rejects.toThrow(ForbiddenException);
    expect(store.rows[0]).toMatchObject({ allowedModels: ['lab-coder'], rpmLimit: 60 });
    expect(cache!.evict).not.toHaveBeenCalled();
  });

  it('an administrator changes limits and widens, to known models only', async () => {
    const { service } = build();
    const { key } = await service.create(userA, { name: 'k', allowedModels: ['lab-coder'] });
    const updated = await service.update(admin, key.id, { limits: { rpm: 500, dailyTokens: 9_000_000 }, allowedModels: [] });
    expect(updated).toMatchObject({ limits: { rpm: 500, concurrent: 4, dailyTokens: 9_000_000 }, allowedModels: [] });
    await expect(service.update(admin, key.id, { allowedModels: ['nope'] })).rejects.toThrow(BadRequestException);
  });

  it("someone else's key does not exist for a non-administrator (404, not 403)", async () => {
    const { service } = build();
    const { key } = await service.create(userA, { name: 'k' });
    await expect(service.update(userB, key.id, { name: 'mine now' })).rejects.toThrow(NotFoundException);
    await expect(service.update(userB, '55555555-5555-4555-8555-555555555555', { name: 'x' })).rejects.toThrow(NotFoundException);
  });

  it('a revoked key cannot be changed (409)', async () => {
    const { service } = build();
    const { key } = await service.create(userA, { name: 'k' });
    await service.revoke(userA, key.id);
    await expect(service.update(userA, key.id, { name: 'x' })).rejects.toThrow(ConflictException);
  });
});

describe('KeyService.revoke', () => {
  it('the owner revokes: revokedAt set, the row kept, the cache evicted, one key_revoked row', async () => {
    const { service, store, cache, audit } = build();
    const { key } = await service.create(userA, { name: 'k' });
    audit.record.mockClear();
    const revoked = await service.revoke(userA, key.id);
    expect(revoked.status).toBe('revoked');
    expect(revoked.revokedAt).toBe(NOW.toISOString());
    expect(store.rows).toHaveLength(1);
    expect(store.rows[0]!.revokedAt).toEqual(NOW);
    expect(cache!.evict).toHaveBeenCalledWith(store.rows[0]!.tokenHash);
    expect(audit.record).toHaveBeenCalledTimes(1);
    expect(audit.record.mock.calls[0]![0]).toMatchObject({ action: AUDIT_GATEWAY_KEY_REVOKED, actorUserId: USER_A, meta: { prefix: key.tokenPrefix, name: 'k' } });
  });

  it("an administrator revokes someone else's key", async () => {
    const { service } = build();
    const { key } = await service.create(userA, { name: 'k' });
    expect((await service.revoke(admin, key.id)).status).toBe('revoked');
  });

  it("another user cannot revoke it (404) and the cache is untouched", async () => {
    const { service, store, cache } = build();
    const { key } = await service.create(userA, { name: 'k' });
    await expect(service.revoke(userB, key.id)).rejects.toThrow(NotFoundException);
    expect(store.rows[0]!.revokedAt).toBeNull();
    expect(cache!.evict).not.toHaveBeenCalled();
  });

  it('a second revoke is idempotent: same answer, no second audit row, the first revokedAt kept', async () => {
    const { service, audit, store } = build();
    const { key } = await service.create(userA, { name: 'k' });
    await service.revoke(userA, key.id);
    audit.record.mockClear();
    store.rows[0]!.revokedAt = new Date('2026-11-04T11:00:00.000Z');
    const again = await service.revoke(userA, key.id);
    expect(again.status).toBe('revoked');
    expect(again.revokedAt).toBe('2026-11-04T11:00:00.000Z');
    expect(audit.record).not.toHaveBeenCalled();
  });

  it('works with no auth cache bound (GW-03 not merged)', async () => {
    const { service } = build({ cache: null });
    const { key } = await service.create(userA, { name: 'k' });
    await expect(service.revoke(userA, key.id)).resolves.toMatchObject({ status: 'revoked' });
  });

  it('a failing cache eviction does not fail the revocation', async () => {
    const { service, store } = build({ cache: { evict: jest.fn().mockRejectedValue(new Error('boom')) } });
    const { key } = await service.create(userA, { name: 'k' });
    await expect(service.revoke(userA, key.id)).resolves.toMatchObject({ status: 'revoked' });
    expect(store.rows[0]!.revokedAt).not.toBeNull();
  });
});
