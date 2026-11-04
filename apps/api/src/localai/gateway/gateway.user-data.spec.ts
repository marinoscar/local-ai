import { userOwnedModelRegistry } from '@marinoscar/platform-api/core';
import { Prisma } from '@prisma/client';

// Fills the registries: the platform's entries, then the app's.
import '../../platform/registrations';
import {
  GATEWAY_KEY_EXPORT_FIELDS,
  GATEWAY_KEY_EXPORT_OMIT,
  GATEWAY_USER_OWNED_MODELS,
  toGatewayKeyExport,
  type GatewayKeyExportSource,
} from './gateway.user-data';

// A hash-shaped value built at run time: it only has to be recognisable in the output.
const HASH = ['h', 'a', 's', 'h'].join('') + '-' + 'ab'.repeat(8);

const row: GatewayKeyExportSource = {
  id: '6b3c1c9e-7a52-4f4e-9d5e-0a1b2c3d4e5f',
  name: 'Desk laptop',
  kind: 'service',
  tokenPrefix: 'lai_ab12cd',
  allowedModels: ['lab-coder', 'qwen3.6-35b-a3b-q4km'],
  rpmLimit: 60,
  concurrentLimit: 4,
  dailyTokenBudget: 5_000_000n,
  createdAt: new Date('2026-11-01T10:00:00.000Z'),
  expiresAt: null,
  revokedAt: new Date('2026-11-02T10:00:00.000Z'),
  lastUsedAt: new Date('2026-11-01T12:30:00.000Z'),
};

describe('the gateway key export mapper', () => {
  it('carries exactly the documented fields', () => {
    expect(Object.keys(toGatewayKeyExport(row))).toEqual([...GATEWAY_KEY_EXPORT_FIELDS]);
  });

  it('formats dates as ISO 8601, keeps null as null and the budget as a number', () => {
    expect(toGatewayKeyExport(row)).toEqual({
      id: row.id,
      name: 'Desk laptop',
      kind: 'service',
      tokenPrefix: 'lai_ab12cd',
      allowedModels: ['lab-coder', 'qwen3.6-35b-a3b-q4km'],
      rpmLimit: 60,
      concurrentLimit: 4,
      dailyTokenBudget: 5_000_000,
      expiresAt: null,
      revokedAt: '2026-11-02T10:00:00.000Z',
      lastUsedAt: '2026-11-01T12:30:00.000Z',
      createdAt: '2026-11-01T10:00:00.000Z',
    });
  });

  it('gives an unlimited budget as null and a budget beyond 2^53 as text', () => {
    expect(toGatewayKeyExport({ ...row, dailyTokenBudget: null }).dailyTokenBudget).toBeNull();
    expect(toGatewayKeyExport({ ...row, dailyTokenBudget: 2n ** 60n }).dailyTokenBudget).toBe((2n ** 60n).toString());
  });

  it('never carries the token hash, even from a row that has one', () => {
    const withHash = { ...row, tokenHash: HASH, userId: 'user-id', createdById: 'admin-id' };
    const out = toGatewayKeyExport(withHash);
    expect(JSON.stringify(out)).not.toContain(HASH);
    expect(out).not.toHaveProperty('tokenHash');
    expect(out).not.toHaveProperty('userId');
    expect(out).not.toHaveProperty('createdById');
  });
});

describe('the gateway key registration', () => {
  const model = Prisma.dmmf.datamodel.models.find((m) => m.name === 'LocalaiGatewayKey');
  const scalars = (model?.fields ?? []).filter((f) => f.kind === 'scalar' || f.kind === 'enum').map((f) => f.name);

  it('is user-owned data that is deleted with the user and exported without the hash', () => {
    expect(userOwnedModelRegistry.get('LocalaiGatewayKey')).toMatchObject({
      ownerField: 'userId',
      purge: 'delete',
      export: 'include',
    });
    expect(GATEWAY_USER_OWNED_MODELS.map((def) => def.model)).toEqual(['LocalaiGatewayKey']);
    expect(GATEWAY_KEY_EXPORT_OMIT).toContain('tokenHash');
  });

  it('exports every column of the table except the omitted ones (a new column forces a decision here)', () => {
    expect(model).toBeDefined();
    const exported = scalars.filter((name) => !(GATEWAY_KEY_EXPORT_OMIT as readonly string[]).includes(name));
    expect(exported).toEqual([...GATEWAY_KEY_EXPORT_FIELDS]);
  });
});
