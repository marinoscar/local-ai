// The Local AI permissions (docs/specs/cloud-app.md §3.4; the three E11 ones: music and workloads), mapped onto the
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
  // E11 (creative workloads): make music on a node from the cloud, delete songs, switch a node's workload.
  MUSIC_USE: {
    id: 'localai_music:use',
    scope: 'org',
    description: 'Make music on a node and play or download the songs of the lab library',
    defaultGrants: ['contributor', 'org_admin'],
  },
  MUSIC_DELETE: {
    id: 'localai_music:delete',
    scope: 'org',
    description: 'Delete songs from the lab music library',
    defaultGrants: ['org_admin'],
  },
  WORKLOADS_WRITE: {
    id: 'localai_workloads:write',
    scope: 'org',
    description: "Switch a node's workload (the set of models it keeps loaded: coding, chat, speech, music)",
    defaultGrants: ['contributor', 'org_admin'],
  },
} as const satisfies PermissionDeclarationMap;

/** The permission ids by constant name: `LOCALAI_PERMISSION_IDS.LAB_OPERATE === 'lab:operate'`. */
export const LOCALAI_PERMISSION_IDS = permissionIds(LOCALAI_PERMISSIONS);

/** One Local AI permission id (`'lab:read' | 'lab:operate' | ...`). */
export type LocalAiPermissionId = (typeof LOCALAI_PERMISSIONS)[keyof typeof LOCALAI_PERMISSIONS]['id'];
