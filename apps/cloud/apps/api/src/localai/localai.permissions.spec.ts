// The Local AI permission catalog (docs/specs/cloud-app.md §3.4): ids, scopes
// and the role matrix, checked against what the seed writes.
import { platformPermissionCatalog } from '@marinoscar/platform-api/manifest';

import { PERMISSION_OPTIONS } from '../platform/permissions';
import { LOCALAI_PERMISSIONS, LOCALAI_PERMISSION_IDS } from './localai.permissions';

type Row = 'admin + org_admin' | 'org_admin only' | 'contributor' | 'viewer';

/** The registered roles that make up each row of the §3.4 table. */
const ROW_ROLES: Readonly<Record<Row, readonly string[]>> = {
  'admin + org_admin': ['admin', 'org_admin'],
  'org_admin only': ['org_admin'],
  contributor: ['contributor'],
  viewer: ['viewer'],
};

/** The §3.4 matrix, written out in full on purpose: a change to a grant must change this table too. */
const MATRIX: Readonly<Record<Row, Readonly<Record<string, boolean>>>> = {
  'admin + org_admin': { 'lab:read': true, 'lab:operate': true, 'lab:admin': true, 'gateway:use': true, 'gateway:admin': true, 'localai_music:use': true, 'localai_music:delete': true, 'localai_workloads:write': true },
  'org_admin only': { 'lab:read': true, 'lab:operate': true, 'lab:admin': false, 'gateway:use': true, 'gateway:admin': false, 'localai_music:use': true, 'localai_music:delete': true, 'localai_workloads:write': true },
  contributor: { 'lab:read': true, 'lab:operate': true, 'lab:admin': false, 'gateway:use': true, 'gateway:admin': false, 'localai_music:use': true, 'localai_music:delete': false, 'localai_workloads:write': true },
  viewer: { 'lab:read': true, 'lab:operate': false, 'lab:admin': false, 'gateway:use': false, 'gateway:admin': false, 'localai_music:use': false, 'localai_music:delete': false, 'localai_workloads:write': false },
};

const catalog = platformPermissionCatalog(PERMISSION_OPTIONS);

function held(row: Row): Set<string> {
  return new Set(ROW_ROLES[row].flatMap((role) => catalog.rolePermissions[role] ?? []));
}

describe('LOCALAI_PERMISSIONS', () => {
  it('keeps the permanent permission ids', () => {
    expect(Object.values(LOCALAI_PERMISSIONS).map((permission) => permission.id)).toEqual([
      'lab:read',
      'lab:operate',
      'lab:admin',
      'gateway:use',
      'gateway:admin',
      'localai_music:use',
      'localai_music:delete',
      'localai_workloads:write',
    ]);
    expect(LOCALAI_PERMISSION_IDS).toEqual({
      LAB_READ: 'lab:read',
      LAB_OPERATE: 'lab:operate',
      LAB_ADMIN: 'lab:admin',
      GATEWAY_USE: 'gateway:use',
      GATEWAY_ADMIN: 'gateway:admin',
      MUSIC_USE: 'localai_music:use',
      MUSIC_DELETE: 'localai_music:delete',
      WORKLOADS_WRITE: 'localai_workloads:write',
    });
  });

  it.each([
    ['lab:read', 'org'],
    ['lab:operate', 'org'],
    ['lab:admin', 'system'],
    ['gateway:use', 'org'],
    ['gateway:admin', 'system'],
    ['localai_music:use', 'org'],
    ['localai_music:delete', 'org'],
    ['localai_workloads:write', 'org'],
  ])('%s has the %s scope', (id, scope) => {
    const declared = Object.values(LOCALAI_PERMISSIONS).find((permission) => permission.id === id);
    expect(declared?.scope).toBe(scope);
    expect(catalog.permissions.find((permission) => permission.name === id)?.scope).toBe(scope);
  });

  it('describes every permission', () => {
    for (const permission of Object.values(LOCALAI_PERMISSIONS)) expect(permission.description.length).toBeGreaterThan(10);
  });

  it('is seeded after the platform permissions, in declaration order', () => {
    const names = catalog.permissions.map((permission) => permission.name);
    const local = names.filter((name) => name.startsWith('lab:') || name.startsWith('gateway:') || name.startsWith('localai_'));
    expect(local).toEqual(Object.values(LOCALAI_PERMISSIONS).map((permission) => permission.id));
    expect(names.slice(-local.length)).toEqual(local);
  });
});

describe('the seeded Local AI grants (spec §3.4 matrix)', () => {
  const cases = (Object.keys(MATRIX) as Row[]).flatMap((row) =>
    Object.entries(MATRIX[row]).map(([permission, allowed]) => [row, permission, allowed] as const),
  );

  it('covers 4 role rows x 8 permissions', () => {
    expect(cases).toHaveLength(32);
  });

  it.each(cases)('%s / %s -> %s', (row, permission, allowed) => {
    expect(held(row).has(permission)).toBe(allowed);
  });

  it('grants no Local AI permission to a role outside the matrix', () => {
    const known = new Set(Object.values(ROW_ROLES).flat());
    const local = new Set<string>(Object.values(LOCALAI_PERMISSIONS).map((permission) => permission.id));
    for (const [role, permissions] of Object.entries(catalog.rolePermissions)) {
      if (known.has(role)) continue;
      expect(permissions.filter((permission) => local.has(permission))).toEqual([]);
    }
  });

  it('keeps system permissions on the system role and org permissions on org roles', () => {
    const systemRoles = new Set(catalog.roles.filter((role) => role.scope === 'system').map((role) => role.name));
    for (const permission of Object.values(LOCALAI_PERMISSIONS)) {
      const holders = Object.entries(catalog.rolePermissions)
        .filter(([, ids]) => ids.includes(permission.id))
        .map(([role]) => role);
      for (const holder of holders) expect(systemRoles.has(holder)).toBe(permission.scope === 'system');
    }
  });
});
