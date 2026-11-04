// Ownership of the Local AI models (docs/specs/cloud-app.md section 3.5), as data for the platform's
// registries (`platform/registrations.ts` registers it beside `app.ownership.ts`).
//
// The lab is one deployment-wide control plane: none of these tables carries an `org_id` and none has
// row-level security. Every model is therefore `system`, except the gateway key, and the only link to a
// person is an ACTOR column (who created, requested or approved), registered below so the
// `userOwnedData` suite can prove each `onDelete` agrees with the purge policy. Gateway keys
// (`localai_gateway_keys`) are the one user-owned model (kind `user`): their registration is
// `gateway/gateway.user-data.ts`, spread into `LOCALAI_USER_OWNED_MODELS` below. The three other gateway tables
// (usage, daily usage, model policies) are deployment-wide `system` data.
import type { ModelOwnershipDef, UserOwnedModelDef } from '@marinoscar/platform-api/core';
import type { Prisma } from '@prisma/client';

import { GATEWAY_USER_OWNED_MODELS } from './gateway/gateway.user-data';

type LocalaiModelName = Extract<
  Prisma.ModelName,
  | 'LocalaiNode'
  | 'LocalaiNodeModel'
  | 'LocalaiCatalogVersion'
  | 'LocalaiCommand'
  | 'LocalaiAlert'
  | 'LocalaiGatewayKey'
  | 'LocalaiGatewayUsage'
  | 'LocalaiGatewayUsageDaily'
  | 'LocalaiModelPolicy'
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
  {
    model: 'LocalaiGatewayKey',
    kind: 'user',
    rationale: "A gateway key belongs to the user it was issued to (user_id, deleted with the user); service keys are issued to a system client by an administrator but are owned by that administrator's account.",
  },
  {
    model: 'LocalaiGatewayUsage',
    kind: 'system',
    rationale:
      'Per-request gateway metadata for the whole lab. key_id is SET NULL when a key goes and user_id is a plain column with no foreign key, so history outlives revocation and a user purge; no prompt or completion text is stored.',
  },
  {
    model: 'LocalaiGatewayUsageDaily',
    kind: 'system',
    rationale: 'Daily gateway rollup per key id; it has no foreign key and is kept forever, whatever happens to the key or its owner.',
  },
  {
    model: 'LocalaiModelPolicy',
    kind: 'system',
    rationale: 'The lab-wide routing policy of one catalog model; updated_by_id only records who changed it.',
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
  ...GATEWAY_USER_OWNED_MODELS,
];
