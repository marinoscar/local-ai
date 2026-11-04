// `GatewayModelResolver` over GW-05's `models/resolve.ts` (docs/specs/gateway.md sections 4.1 and 4.4): `ModelResolver.resolve`
// (alias -> catalog id, approved catalog, policy enabled), then `assertKeyAllows` (403 LAI-GW-012 for an inference
// request; `GET /v1/models/{id}` answers 404 for the same case, that is the listing's rule). The roles are the approved
// catalog's, so the `embedding` check of `/v1/embeddings` does not depend on what a node happens to report.
import { Injectable } from '@nestjs/common';

import type { GatewayKeyRecord } from './auth';
import { GatewayError } from './errors';
import type { GatewayModelResolver, ResolvedGatewayModel } from './model-resolver';
import { assertKeyAllows, ModelResolver } from './models/resolve';

@Injectable()
export class CatalogModelResolver implements GatewayModelResolver {
  constructor(private readonly models: ModelResolver) {}

  async resolve(requested: string, key: GatewayKeyRecord): Promise<ResolvedGatewayModel> {
    const resolved = await this.models.resolve(requested);
    if (!resolved) throw new GatewayError('LAI-GW-020', { param: 'model' });
    assertKeyAllows(key, requested, resolved);
    const { policy, hints } = resolved;
    return {
      modelId: resolved.id,
      requestedModel: requested,
      policy: { preferredNodes: policy.preferredNodes, onDemandLoad: policy.onDemandLoad, loadWaitS: policy.loadWaitS, firstByteTimeoutS: policy.firstByteTimeoutS },
      // A catalog entry with no roles at all counts as "unknown", not as "has none".
      roles: hints.roles.length > 0 ? hints.roles : null,
      inApprovedCatalog: true,
    };
  }
}
