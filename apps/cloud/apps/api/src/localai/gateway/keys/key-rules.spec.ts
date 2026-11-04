import { BadRequestException, ForbiddenException } from '@nestjs/common';

import type { GatewayKeyDefaults } from '../../settings.service';
import { DAY_MS, dedupe, isNarrowing, resolveCreate, resolvePatch, type KeyActor } from './key-rules';

const DEFAULTS: GatewayKeyDefaults = {
  user: { rpm: 60, concurrent: 4, dailyTokens: 2_000_000, expiresInDays: 90 },
  service: { rpm: 600, concurrent: 16, dailyTokens: null, expiresInDays: null },
  maxUserKeyDays: 365,
};
const NOW = new Date('2026-11-04T12:00:00.000Z');
const OWNER = '11111111-1111-4111-8111-111111111111';
const OTHER = '22222222-2222-4222-8222-222222222222';

const contributor: KeyActor = { userId: OWNER, permissions: ['gateway:use', 'lab:read'] };
const admin: KeyActor = { userId: OTHER, permissions: ['gateway:use', 'gateway:admin', 'lab:admin'] };

describe('isNarrowing (empty = all)', () => {
  it.each([
    [[], [], true],
    [[], ['a'], true],
    [['a', 'b'], ['a'], true],
    [['a', 'b'], ['b', 'a'], true],
    [['a'], [], false],
    [['a'], ['a', 'b'], false],
    [['a'], ['b'], false],
  ] as Array<[string[], string[], boolean]>)('%j -> %j is narrowing: %s', (before, after, expected) => {
    expect(isNarrowing(before, after)).toBe(expected);
  });

  it('dedupe keeps first-seen order', () => {
    expect(dedupe(['b', 'a', 'b', 'c', 'a'])).toEqual(['b', 'a', 'c']);
  });
});

describe('resolveCreate: the permission matrix', () => {
  const create = (body: Parameters<typeof resolveCreate>[0], actor: KeyActor) => resolveCreate(body, actor, DEFAULTS, NOW);

  it('a contributor gets a user key with the user defaults and the default expiry', () => {
    const key = create({ name: 'OpenCode laptop' }, contributor);
    expect(key).toMatchObject({ kind: 'user', ownerUserId: OWNER, rpm: 60, concurrent: 4, dailyTokens: 2_000_000, allowedModels: [] });
    expect(key.expiresAt).toEqual(new Date(NOW.getTime() + 90 * DAY_MS));
  });

  it('a contributor may choose lower limits and a shorter or maximal expiry', () => {
    const key = create({ name: 'k', limits: { rpm: 10, concurrent: 1, dailyTokens: 1000 }, expiresInDays: 365, allowedModels: ['a', 'a', 'b'] }, contributor);
    expect(key).toMatchObject({ rpm: 10, concurrent: 1, dailyTokens: 1000, allowedModels: ['a', 'b'] });
    expect(key.expiresAt).toEqual(new Date(NOW.getTime() + 365 * DAY_MS));
  });

  it.each([
    ['a service key', { name: 'k', kind: 'service' as const }],
    ['a key for someone else', { name: 'k', ownerUserId: OTHER }],
    ['more requests per minute', { name: 'k', limits: { rpm: 61 } }],
    ['more concurrent requests', { name: 'k', limits: { concurrent: 5 } }],
    ['a larger daily budget', { name: 'k', limits: { dailyTokens: 2_000_001 } }],
  ])('a contributor cannot create %s (403)', (_label, body) => {
    expect(() => create(body, contributor)).toThrow(ForbiddenException);
  });

  it('a contributor naming themself as owner is fine', () => {
    expect(create({ name: 'k', ownerUserId: OWNER }, contributor).ownerUserId).toBe(OWNER);
  });

  it('an administrator may create a service key, with no expiry and no budget by default', () => {
    const key = create({ name: 'open-webui', kind: 'service' }, admin);
    expect(key).toMatchObject({ kind: 'service', ownerUserId: OTHER, rpm: 600, concurrent: 16, dailyTokens: null, expiresAt: null });
  });

  it('a service key may take explicit limits, an explicit expiry and null for both', () => {
    const key = create({ name: 's', kind: 'service', expiresInDays: 30, limits: { rpm: 5000, dailyTokens: 50_000_000 } }, admin);
    expect(key).toMatchObject({ rpm: 5000, dailyTokens: 50_000_000 });
    expect(key.expiresAt).toEqual(new Date(NOW.getTime() + 30 * DAY_MS));
    expect(create({ name: 's', kind: 'service', expiresInDays: null, limits: { dailyTokens: null } }, admin)).toMatchObject({ expiresAt: null, dailyTokens: null });
  });

  it('an administrator may create a user key for someone else and raise its limits', () => {
    const key = create({ name: 'k', ownerUserId: OWNER, limits: { rpm: 1000, dailyTokens: 10_000_000 } }, admin);
    expect(key).toMatchObject({ kind: 'user', ownerUserId: OWNER, rpm: 1000, dailyTokens: 10_000_000 });
  });

  describe('a user key can never be unlimited or immortal, whoever creates it', () => {
    it.each([contributor, admin])('no budget is a 400 (%#)', (actor) => {
      expect(() => create({ name: 'k', limits: { dailyTokens: null } }, actor)).toThrow(BadRequestException);
    });
    it.each([contributor, admin])('no expiry is a 400 (%#)', (actor) => {
      expect(() => create({ name: 'k', expiresInDays: null }, actor)).toThrow(BadRequestException);
    });
  });

  describe('the expiry bounds of a user key', () => {
    it('accepts 1 and the maximum', () => {
      expect(create({ name: 'k', expiresInDays: 1 }, contributor).expiresAt).toEqual(new Date(NOW.getTime() + DAY_MS));
      expect(create({ name: 'k', expiresInDays: 365 }, contributor).expiresAt).toEqual(new Date(NOW.getTime() + 365 * DAY_MS));
    });
    it.each([366, 3650, 0, -1])('refuses %s days (400)', (days) => {
      expect(() => create({ name: 'k', expiresInDays: days }, contributor)).toThrow(BadRequestException);
    });
    it('follows gateway.maxUserKeyDays', () => {
      const short = { ...DEFAULTS, maxUserKeyDays: 30 };
      expect(() => resolveCreate({ name: 'k', expiresInDays: 31 }, contributor, short, NOW)).toThrow(BadRequestException);
      expect(resolveCreate({ name: 'k', expiresInDays: 30 }, contributor, short, NOW).expiresAt).toEqual(new Date(NOW.getTime() + 30 * DAY_MS));
    });
    it('clamps a default longer than the maximum to the maximum', () => {
      const short = { ...DEFAULTS, maxUserKeyDays: 30 };
      expect(resolveCreate({ name: 'k' }, contributor, short, NOW).expiresAt).toEqual(new Date(NOW.getTime() + 30 * DAY_MS));
    });
  });

  it('takes its defaults from the settings it is given (changing them changes the next key)', () => {
    const changed: GatewayKeyDefaults = { ...DEFAULTS, user: { rpm: 20, concurrent: 2, dailyTokens: 500, expiresInDays: 7 } };
    const key = resolveCreate({ name: 'k' }, contributor, changed, NOW);
    expect(key).toMatchObject({ rpm: 20, concurrent: 2, dailyTokens: 500 });
    expect(key.expiresAt).toEqual(new Date(NOW.getTime() + 7 * DAY_MS));
    // and a limit that was fine before is now above the default
    expect(() => resolveCreate({ name: 'k', limits: { rpm: 60 } }, contributor, changed, NOW)).toThrow(ForbiddenException);
  });
});

describe('resolvePatch: the permission matrix', () => {
  const target = (allowedModels: string[], kind: 'user' | 'service' = 'user') => ({ userId: OWNER, kind, allowedModels });
  const patch = (t: ReturnType<typeof target>, body: Parameters<typeof resolvePatch>[1], actor: KeyActor) => resolvePatch(t, body, actor);

  it('the owner renames', () => {
    expect(patch(target([]), { name: 'New name' }, contributor)).toEqual({ changes: { name: 'New name' }, changed: ['name'], addedModels: [] });
  });

  it('the owner narrows: all -> a list, a list -> a subset', () => {
    expect(patch(target([]), { allowedModels: ['a'] }, contributor).changes).toEqual({ allowedModels: ['a'] });
    expect(patch(target(['a', 'b']), { allowedModels: ['b'] }, contributor).addedModels).toEqual([]);
  });

  it.each([
    ['a list -> all', ['a'], []],
    ['a list -> a list with something new', ['a'], ['a', 'b']],
    ['a list -> a different list', ['a'], ['c']],
  ])('the owner cannot widen: %s (403)', (_label, before, after) => {
    expect(() => patch(target(before), { allowedModels: after }, contributor)).toThrow(ForbiddenException);
  });

  it.each([{ rpm: 1 }, { concurrent: 1 }, { dailyTokens: 1 }, { rpm: 1000 }])('the owner cannot change limits, not even lower them (403): %j', (limits) => {
    expect(() => patch(target([]), { limits }, contributor)).toThrow(ForbiddenException);
  });

  it('an administrator changes limits and widens, and the new entries are reported for the known-model check', () => {
    const result = patch(target(['a']), { allowedModels: ['a', 'b'], limits: { rpm: 500, concurrent: 8, dailyTokens: 9 } }, admin);
    expect(result.changes).toEqual({ allowedModels: ['a', 'b'], rpmLimit: 500, concurrentLimit: 8, dailyTokenBudget: 9n });
    expect(result.changed).toEqual(['allowedModels', 'rpm', 'concurrent', 'dailyTokens']);
    expect(result.addedModels).toEqual(['b']);
  });

  it('only an empty-to-list narrowing reports every entry as added (all had no entries)', () => {
    expect(patch(target([]), { allowedModels: ['a', 'b'] }, contributor).addedModels).toEqual(['a', 'b']);
  });

  it('removing the budget: a service key may, a user key may not (not even an administrator)', () => {
    expect(patch(target([], 'service'), { limits: { dailyTokens: null } }, admin).changes).toEqual({ dailyTokenBudget: null });
    expect(() => patch(target([], 'user'), { limits: { dailyTokens: null } }, admin)).toThrow(BadRequestException);
  });
});
