// The five Local AI permissions (docs/specs/cloud-app.md §3.4), mapped onto the
// platform's roles through `defaultGrants`. Pure data: `src/platform/permissions.ts`
// adds it to the seeded catalog, so the seed (`prisma/seed.ts`) and the
// registries (`src/platform/registrations.ts`) never disagree.
//
// The ids are permanent strings: they are stored in `permissions` and
// `role_permissions`, and every cloud UI and gateway route is gated by one.
// A `nod_` node credential has no organization, so it receives none of the
// `org` permissions and node routes never rely on these (they use the binding
// guard, spec §4.2).
import type { PermissionDeclarationMap } from '@marinoscar/platform-api/core';
import { permissionIds } from '@marinoscar/platform-api/core';

export const LOCALAI_PERMISSIONS = {
  LAB_READ: {
    id: 'lab:read',
    scope: 'org',
    description: 'See nodes, models, ark readiness, alerts and command history',
    defaultGrants: ['viewer', 'contributor', 'org_admin'],
  },
  LAB_OPERATE: {
    id: 'lab:operate',
    scope: 'org',
    description: 'Send non-destructive commands to nodes (load, unload, sync, restart, logs)',
    defaultGrants: ['contributor', 'org_admin'],
  },
  LAB_ADMIN: {
    id: 'lab:admin',
    scope: 'system',
    description: 'Add and retire nodes, approve catalogs, destructive commands, audit',
    defaultGrants: ['admin'],
  },
  GATEWAY_USE: {
    id: 'gateway:use',
    scope: 'org',
    description: 'Create own gateway keys and call https://ai.marin.cr/v1',
    defaultGrants: ['contributor', 'org_admin'],
  },
  GATEWAY_ADMIN: {
    id: 'gateway:admin',
    scope: 'system',
    description: 'Manage every gateway key, model policies and usage',
    defaultGrants: ['admin'],
  },
} as const satisfies PermissionDeclarationMap;

/** The permission ids by constant name: `LOCALAI_PERMISSION_IDS.LAB_OPERATE === 'lab:operate'`. */
export const LOCALAI_PERMISSION_IDS = permissionIds(LOCALAI_PERMISSIONS);

/** One Local AI permission id (`'lab:read' | 'lab:operate' | ...`). */
export type LocalAiPermissionId = (typeof LOCALAI_PERMISSIONS)[keyof typeof LOCALAI_PERMISSIONS]['id'];
