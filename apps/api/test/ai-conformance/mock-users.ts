// The users the AI conformance suites authenticate as, registered in the Prisma
// mock the way the identity slice reads them: `user.findUnique` answers the
// full graph (system roles, memberships and their org roles, each with its
// permissions) whatever the `include`, which is all a JWT or a PAT needs.
//
// The role -> permission map is NOT hand-written: it is the grants this app
// seeds (`buildPermissionCatalog()` after `src/platform/registrations`), so the
// RBAC matrix checks the real catalog.
import { randomUUID } from 'node:crypto';
import { JwtService } from '@nestjs/jwt';
import { buildPermissionCatalog, catalogGrants } from '@marinoscar/platform-api/core';

import '../../src/platform/registrations';
import { MOCK_DEFAULT_ORG_ID, prismaMock } from './prisma.mock';

/** The roles the suites ask for. `admin` is the system administrator who is also an organization administrator. */
export type FixtureRole = 'admin' | 'contributor' | 'viewer';

/** The registered roles that make up each fixture role: `[system role?, org role]`. */
const ROLE_PARTS: Readonly<Record<FixtureRole, { system: string | null; org: string }>> = {
  admin: { system: 'admin', org: 'org_admin' },
  contributor: { system: null, org: 'contributor' },
  viewer: { system: null, org: 'viewer' },
};

function grantsOf(role: string): string[] {
  return catalogGrants(buildPermissionCatalog())
    .filter((grant) => grant.role === role)
    .map((grant) => grant.permission);
}

/** The permissions each fixture role holds, as this app seeds them (system plus organization grants). */
export function fixtureRolePermissions(): Record<FixtureRole, readonly string[]> {
  const out = {} as Record<FixtureRole, readonly string[]>;
  for (const [role, parts] of Object.entries(ROLE_PARTS) as Array<[FixtureRole, (typeof ROLE_PARTS)[FixtureRole]]>) {
    out[role] = [...new Set([...(parts.system ? grantsOf(parts.system) : []), ...grantsOf(parts.org)])];
  }
  return out;
}

function roleRow(name: string, scope: 'system' | 'org') {
  return {
    id: `role-${name}`,
    name,
    description: null,
    scope,
    rolePermissions: grantsOf(name).map((permission) => ({
      roleId: `role-${name}`,
      permissionId: `perm-${permission}`,
      permission: { id: `perm-${permission}`, name: permission, description: null, scope },
    })),
  };
}

const registry = new Map<string, Record<string, unknown>>();

function buildUser(id: string, role: FixtureRole): Record<string, unknown> {
  const parts = ROLE_PARTS[role];
  const now = new Date();
  return {
    id,
    email: `${role}-${id.slice(0, 8)}@example.test`,
    isActive: true,
    displayName: `${role} ${id.slice(0, 8)}`,
    profileImageUrl: null,
    providerDisplayName: null,
    providerProfileImageUrl: null,
    createdAt: now,
    updatedAt: now,
    userRoles: parts.system ? [{ userId: id, roleId: `role-${parts.system}`, role: roleRow(parts.system, 'system') }] : [],
    memberships: [
      {
        id: randomUUID(),
        orgId: MOCK_DEFAULT_ORG_ID,
        userId: id,
        status: 'active',
        lastActiveAt: now,
        createdAt: now,
        updatedAt: now,
        roleId: `role-${parts.org}`,
        org: { id: MOCK_DEFAULT_ORG_ID, isDefault: true },
        role: roleRow(parts.org, 'org'),
      },
    ],
  };
}

/** Forgets every registered user (a suite's `reset()`). */
export function clearMockUsers(): void {
  registry.clear();
}

/**
 * The stubs every request needs before any test stubs its own: the user
 * registry behind `user.findUnique`, and the default organization.
 */
export function installBaseMocks(): void {
  prismaMock.user.findUnique.mockImplementation(async ({ where }: { where: { id?: string } }) => (where.id ? registry.get(where.id) ?? null : null));
  prismaMock.organization.findFirst.mockResolvedValue({ id: MOCK_DEFAULT_ORG_ID, name: 'Default', slug: 'default', isDefault: true });
}

/** Registers a user with `role` (optionally a fixed `id`) and signs an access token the identity guard accepts. */
export function registerMockUser(jwt: JwtService, role: FixtureRole, id: string = randomUUID()): { id: string; accessToken: string } {
  const user = buildUser(id, role);
  registry.set(id, user);
  const roles = (user.userRoles as Array<{ role: { name: string } }>).map((userRole) => userRole.role.name);
  const accessToken = jwt.sign({ sub: id, email: user.email, roles, org: MOCK_DEFAULT_ORG_ID });
  return { id, accessToken };
}
