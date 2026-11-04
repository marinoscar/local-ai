// The gateway's proxy slice (GW-06): the upstream client, the attempt loop and `GatewayService`, resolved by
// `registerGatewayRoutes` with `app.get(GatewayService, { strict: false })`.
//
// Every extension point is a provider token with a default here; the item that builds on it rebinds ONE token:
//   GATEWAY_MODEL_RESOLVER  GW-05: models/resolve.ts adapter (CatalogModelResolver)
//   GATEWAY_LIMITS          GW-08: RPM, concurrency, daily budget
//   GATEWAY_USAGE_SINK      GW-08: the usage recorder
//   GATEWAY_STREAM_HANDLER  GW-07: SSE passthrough
//   UPSTREAM_TRANSPORT      GW-10: chosen by LOCALAI_GATEWAY_UPSTREAM (public | tailnet), see upstream/transport.factory.ts
//   GATEWAY_ATTEMPT_HOOKS   GW-07: observe the loop
//   GATEWAY_CANDIDATE_SOURCE  GW-09: the table's source wrapped by the on-demand load (routing/on-demand.ts)
import { Logger, Module } from '@nestjs/common';
import { resolveApiVersion } from '@marinoscar/platform-api/host';

import { LOCALAI_COMMAND_ENQUEUER, type LocalaiCommandEnqueuer } from '../commands/command-enqueuer';
import { CommandEnqueuerModule } from '../commands/command-enqueuer.module';

import { AttemptLoop, TableCandidateSource, type AttemptHooks, type CandidateSource } from './attempts';
import { GatewayService } from './gateway.service';
import { GATEWAY_LIMITS, noopLimits } from './limits';
import { GATEWAY_MODEL_RESOLVER } from './model-resolver';
import { CatalogModelResolver } from './model-resolver.catalog';
import { GatewayModelsModule } from './models/models.module';
import { GATEWAY_PROXY_SETTINGS, PrismaProxySettingsReader } from './proxy-settings';
import { AuditLoadAudit, PrismaActiveLoadReader } from './routing/on-demand.adapters';
import { ACTIVE_LOAD_READER, OnDemandCandidateSource, type ActiveLoadReader, type LoadAudit } from './routing/on-demand';
import { NodeKeyProvider } from './routing/node-key.provider';
import { GatewayRoutingModule } from './routing/routing.module';
import { RoutingTable } from './routing/routing-table';
import { GATEWAY_STREAM_HANDLER, streamingNotAvailable } from './stream-handler';
import { createUpstreamTransport } from './upstream/transport.factory';
import { UPSTREAM_TRANSPORT, type UpstreamTransport } from './upstream/client';
import { PrismaTailnetSuffixReader } from './upstream/tailnet-suffix';
import { GATEWAY_USAGE_SINK, noopUsageSink } from './usage/usage-sink';

/** Optional providers: bind these tokens to observe the attempt loop or to replace the candidate source. */
export const GATEWAY_ATTEMPT_HOOKS = Symbol('LOCALAI_GATEWAY_ATTEMPT_HOOKS');
export const GATEWAY_CANDIDATE_SOURCE = Symbol('LOCALAI_GATEWAY_CANDIDATE_SOURCE');

@Module({
  imports: [GatewayRoutingModule, GatewayModelsModule, CommandEnqueuerModule],
  providers: [
    CatalogModelResolver,
    PrismaProxySettingsReader,
    PrismaTailnetSuffixReader,
    { provide: GATEWAY_MODEL_RESOLVER, useExisting: CatalogModelResolver },
    { provide: GATEWAY_PROXY_SETTINGS, useExisting: PrismaProxySettingsReader },
    { provide: GATEWAY_LIMITS, useValue: noopLimits },
    { provide: GATEWAY_USAGE_SINK, useValue: noopUsageSink },
    { provide: GATEWAY_STREAM_HANDLER, useValue: streamingNotAvailable },
    { provide: GATEWAY_ATTEMPT_HOOKS, useValue: {} satisfies AttemptHooks },
    PrismaActiveLoadReader,
    AuditLoadAudit,
    { provide: ACTIVE_LOAD_READER, useExisting: PrismaActiveLoadReader },
    {
      // GW-09: the table's own source (steps 1, 2 and 4) wrapped by step 3, the on-demand load.
      provide: GATEWAY_CANDIDATE_SOURCE,
      inject: [RoutingTable, LOCALAI_COMMAND_ENQUEUER, ACTIVE_LOAD_READER, AuditLoadAudit],
      useFactory: (table: RoutingTable, commands: LocalaiCommandEnqueuer, loads: ActiveLoadReader, audit: LoadAudit): CandidateSource => {
        const logger = new Logger('GatewayOnDemand');
        return new OnDemandCandidateSource({ inner: new TableCandidateSource(table), routing: table, commands, loads, audit, log: { warn: (line) => logger.warn(line) } });
      },
    },
    {
      provide: UPSTREAM_TRANSPORT,
      inject: [PrismaTailnetSuffixReader],
      useFactory: (tailnetSuffix: PrismaTailnetSuffixReader): UpstreamTransport => createUpstreamTransport({ version: resolveApiVersion(__dirname), tailnetSuffix }),
    },
    {
      provide: AttemptLoop,
      inject: [RoutingTable, NodeKeyProvider, UPSTREAM_TRANSPORT, GATEWAY_CANDIDATE_SOURCE, GATEWAY_ATTEMPT_HOOKS],
      useFactory: (routing: RoutingTable, keys: NodeKeyProvider, transport: UpstreamTransport, source: CandidateSource, hooks: AttemptHooks) => {
        const logger = new Logger('GatewayAttempts');
        return new AttemptLoop({ routing, keys, transport, source, hooks, log: { warn: (line) => logger.warn(line) } });
      },
    },
    GatewayService,
  ],
  exports: [GatewayService, AttemptLoop],
})
export class GatewayProxyModule {}
