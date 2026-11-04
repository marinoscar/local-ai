// The gateway's routing slice (GW-04): the in-memory routing table, the endpoint probe and the node key lookup. Exports
// what the attempt loop (GW-06), `/v1/models` (GW-05) and the on-demand load (GW-09) use. `PrismaService` and the event
// bus are global; the alert engine and the node keys are imported.
import { Module } from '@nestjs/common';

import { AlertsModule } from '../../alerts/alerts.module';
import { LOCALAI_CLOCK, systemLocalaiClock } from '../../commands/command-events';
import { NodeKeysModule } from '../../keys/node-keys.module';
import { LabNodesModule } from '../../nodes/lab-nodes.module';
import { AlertsServiceEndpointAlerts, ENDPOINT_ALERTS } from './endpoint-alerts.port';
import { NodeKeyProvider } from './node-key.provider';
import { EndpointProbe, fetchHealthClient, HEALTH_CLIENT } from './probe';
import { PrismaRoutingSource, ROUTING_SOURCE } from './routing-source';
import { ROUTING_OPTIONS, RoutingTable, routingOptionsFromEnv } from './routing-table';

@Module({
  imports: [AlertsModule, NodeKeysModule, LabNodesModule],
  providers: [
    RoutingTable,
    EndpointProbe,
    NodeKeyProvider,
    PrismaRoutingSource,
    AlertsServiceEndpointAlerts,
    { provide: ROUTING_SOURCE, useExisting: PrismaRoutingSource },
    { provide: ENDPOINT_ALERTS, useExisting: AlertsServiceEndpointAlerts },
    { provide: HEALTH_CLIENT, useValue: fetchHealthClient },
    { provide: ROUTING_OPTIONS, useFactory: () => routingOptionsFromEnv() },
    { provide: LOCALAI_CLOCK, useValue: systemLocalaiClock },
  ],
  exports: [RoutingTable, EndpointProbe, NodeKeyProvider],
})
export class GatewayRoutingModule {}
