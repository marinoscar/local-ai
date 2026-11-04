// The gateway's model slice (GW-05): the approved catalog's model hints, the per-model policies, the resolution of a
// requested name, and the `/v1/models` listing. Exports what the inference handlers (GW-06) and the policy page (GW-09)
// use. `PrismaService` is global; the routing table comes from the routing slice.
import { Module } from '@nestjs/common';

import { LOCALAI_CLOCK, systemLocalaiClock } from '../../commands/command-events';
import { GatewayRoutingModule } from '../routing/routing.module';
import { APPROVED_CATALOG_STORE, ApprovedCatalogReader, PrismaApprovedCatalogStore } from './catalog-hints';
import { ModelListing } from './listing';
import { MODEL_POLICY_SOURCE, PrismaModelPolicySource } from './policy';
import { ModelDirectoryProvider, ModelResolver } from './resolve';

@Module({
  imports: [GatewayRoutingModule],
  providers: [
    ApprovedCatalogReader,
    PrismaApprovedCatalogStore,
    PrismaModelPolicySource,
    ModelDirectoryProvider,
    ModelResolver,
    ModelListing,
    { provide: APPROVED_CATALOG_STORE, useExisting: PrismaApprovedCatalogStore },
    { provide: MODEL_POLICY_SOURCE, useExisting: PrismaModelPolicySource },
    { provide: LOCALAI_CLOCK, useValue: systemLocalaiClock },
  ],
  exports: [ModelResolver, ModelListing, ModelDirectoryProvider, ApprovedCatalogReader],
})
export class GatewayModelsModule {}
