// Model policies: the list/upsert routes. `PrismaService`, `AUDIT_SINK` and the event bus are global; the directory and
// the routing table come from the models slice.
import { Module } from '@nestjs/common';

import { GatewayModelsModule } from '../models/models.module';
import { GatewayRoutingModule } from '../routing/routing.module';
import { PoliciesController } from './policies.controller';
import { PoliciesService } from './policies.service';
import { PolicyCacheListener } from './policy-cache.listener';
import { GATEWAY_POLICY_STORE, PrismaPolicyStore } from './policy.store';

@Module({
  imports: [GatewayModelsModule, GatewayRoutingModule],
  controllers: [PoliciesController],
  providers: [PoliciesService, PolicyCacheListener, PrismaPolicyStore, { provide: GATEWAY_POLICY_STORE, useExisting: PrismaPolicyStore }],
  exports: [PoliciesService],
})
export class GatewayPoliciesModule {}
