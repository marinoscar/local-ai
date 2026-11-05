// The gateway's routing slice (GW-04): the in-memory routing table, the endpoint probe and the node key lookup. Exports
// what the attempt loop (GW-06), `/v1/models` (GW-05) and the on-demand load (GW-09) use. `PrismaService` and the event
// bus are global; the alert engine and the node keys are imported.
import { Module } from '@nestjs/common';

import { AlertsModule } from '../../alerts/alerts.module';
import { LOCALAI_CLOCK, systemLocalaiClock } from '../../commands/command-events';
import { NodeKeyService } from '../../keys/node-key.service';
import { NodeKeysModule } from '../../keys/node-keys.module';
import { LabNodesModule } from '../../nodes/lab-nodes.module';
import { AlertsServiceEndpointAlerts, ENDPOINT_ALERTS } from './endpoint-alerts.port';
import { endpointAlertsFor, healthClientFor, nodeKeysFor, routingSourceFor } from './fake-upstream.seed';
import { NodeKeyProvider } from './node-key.provider';
import { EndpointProbe, fetchHealthClient, HEALTH_CLIENT } from './probe';
import { PrismaRoutingSource, ROUTING_SOURCE } from './routing-source';
import { ROUTING_OPTIONS, RoutingTable, routingOptionsFromEnv } from './routing-table';

@Module({
  imports: [AlertsModule, NodeKeysModule, LabNodesModule],
  providers: [
    RoutingTable,
    EndpointProbe,
    PrismaRoutingSource,
    AlertsServiceEndpointAlerts,
    // The four seams below are the real providers unless the load-test flag is on (fake-upstream.seed.ts, refused in production).
    { provide: NodeKeyProvider, inject: [NodeKeyService], useFactory: (keys: NodeKeyService) => nodeKeysFor(new NodeKeyProvider(keys)) },
    { provide: ROUTING_SOURCE, inject: [PrismaRoutingSource], useFactory: (real: PrismaRoutingSource) => routingSourceFor(real) },
    { provide: ENDPOINT_ALERTS, inject: [AlertsServiceEndpointAlerts], useFactory: (real: AlertsServiceEndpointAlerts) => endpointAlertsFor(real) },
    { provide: HEALTH_CLIENT, useFactory: () => healthClientFor(fetchHealthClient) },
    { provide: ROUTING_OPTIONS, useFactory: () => routingOptionsFromEnv() },
    { provide: LOCALAI_CLOCK, useValue: systemLocalaiClock },
  ],
  exports: [RoutingTable, EndpointProbe, NodeKeyProvider],
})
export class GatewayRoutingModule {}
