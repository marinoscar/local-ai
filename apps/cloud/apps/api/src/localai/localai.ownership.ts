// Ownership of the Local AI models (docs/specs/cloud-app.md section 3.5), as data for the platform's
// registries (`platform/registrations.ts` registers it beside `app.ownership.ts`).
//
// The lab is one deployment-wide control plane: none of these tables carries an `org_id`, none has
// row-level security, and none is personal to a user. Every model is therefore `system`, and the only
// link to a person is an ACTOR column (who created, requested or approved), registered below so the
// `userOwnedData` suite can prove each `onDelete` agrees with the purge policy. Gateway keys
// (`localai_gateway_keys`) are user-owned and are registered by GW-01, not here.
import type { ModelOwnershipDef, UserOwnedModelDef } from '@marinoscar/platform-api/core';
import type { Prisma } from '@prisma/client';

type LocalaiModelName = Extract<
  Prisma.ModelName,
  'LocalaiNode' | 'LocalaiNodeModel' | 'LocalaiCatalogVersion' | 'LocalaiCommand' | 'LocalaiAlert'
>;

export const LOCALAI_MODEL_OWNERSHIP: readonly ModelOwnershipDef<LocalaiModelName>[] = [
  {
    model: 'LocalaiNode',
    kind: 'system',
    rationale: 'A lab node is deployment infrastructure, not a tenant or personal record; created_by_id only records who added it.',
  },
  {
    model: 'LocalaiNodeModel',
    kind: 'system',
    rationale: "Per-node model state projected from the node's snapshot; it follows its node and has no owner.",
  },
  {
    model: 'LocalaiCatalogVersion',
    kind: 'system',
    rationale: 'The lab-wide model catalog; one version is approved for the whole deployment.',
  },
  {
    model: 'LocalaiCommand',
    kind: 'system',
    rationale: 'A command for one node of the lab; requested_by_id only records who asked.',
  },
  {
    model: 'LocalaiAlert',
    kind: 'system',
    rationale: 'A lab-wide or per-node alert; it is raised by the system and acknowledged by an administrator.',
  },
];

export const LOCALAI_USER_OWNED_MODELS: readonly UserOwnedModelDef<LocalaiModelName>[] = [
  {
    model: 'LocalaiNode',
    actorFields: ['createdById'],
    purge: 'detach',
    export: 'exclude',
    rationale: 'Lab infrastructure; it records who added the node. The node outlives that user (SetNull).',
  },
  {
    model: 'LocalaiCatalogVersion',
    actorFields: ['createdById', 'approvedById'],
    purge: 'detach',
    export: 'exclude',
    rationale: 'A lab-wide catalog version; it records who imported and who approved it, neither of whom owns it (SetNull).',
  },
  {
    model: 'LocalaiCommand',
    actorFields: ['requestedById'],
    purge: 'detach',
    export: 'exclude',
    rationale: 'A command history row for a node; it records who asked and outlives that user (SetNull).',
  },
];
