import type { CreatedKey, GatewayKey } from './types';

export const KEYS_NOW = new Date('2026-11-04T12:00:00.000Z');

// A made-up token, built at run time so no key-shaped literal sits in the source.
export const FIXTURE_TOKEN = `lai_${'Zq'.repeat(21)}Z`;

export function keyFixture(patch: Partial<GatewayKey> = {}): GatewayKey {
  return {
    id: '00000000-0000-4000-8000-000000000001',
    name: 'OpenCode laptop',
    kind: 'user',
    tokenPrefix: 'lai_Ab3dEf',
    allowedModels: [],
    limits: { rpm: 60, concurrent: 4, dailyTokens: 2_000_000 },
    expiresAt: '2027-01-31T12:00:00.000Z',
    revokedAt: null,
    lastUsedAt: '2026-11-04T11:58:00.000Z',
    createdAt: '2026-11-01T10:00:00.000Z',
    status: 'active',
    ownerUserId: '00000000-0000-4000-8000-0000000000aa',
    ...patch,
  };
}

export function keysFixture(): GatewayKey[] {
  return [
    keyFixture(),
    keyFixture({
      id: '00000000-0000-4000-8000-000000000002',
      name: 'open-webui',
      kind: 'service',
      tokenPrefix: 'lai_Q9xk2L',
      limits: { rpm: 600, concurrent: 16, dailyTokens: null },
      expiresAt: null,
      lastUsedAt: '2026-11-04T11:59:58.000Z',
    }),
  ];
}

export function createdFixture(patch: Partial<GatewayKey> = {}): CreatedKey {
  return { key: keyFixture({ id: '00000000-0000-4000-8000-000000000003', name: 'New key', tokenPrefix: FIXTURE_TOKEN.slice(0, 10), lastUsedAt: null, ...patch }), token: FIXTURE_TOKEN };
}
