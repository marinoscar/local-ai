// INTERIM `GatewayModelResolver` over Prisma, in place until GW-05 (#140) lands its `models/resolve.ts` (see
// `model-resolver.ts`). Aliases and `enabled` come from `localai_model_policies`; a model is known when the approved
// catalog lists it or a node row mentions it; roles are the union of the roles the nodes report. Reads are cached 10 s.
import { Injectable, Logger } from '@nestjs/common';

import { PrismaService } from '../../prisma/prisma.service';
import { modelSizesFromFiles } from '../nodes/catalog-model-sizes';
import type { GatewayKeyRecord } from './auth';
import { TtlCache } from './auth-cache';
import { GatewayError } from './errors';
import { DEFAULT_MODEL_POLICY, keyAllowsModel, type GatewayModelResolver, type ProxyModelPolicy, type ResolvedGatewayModel } from './model-resolver';

interface ModelWorld {
  policies: Map<string, ProxyModelPolicy & { enabled: boolean }>;
  aliases: Map<string, string>;
  catalogIds: Set<string>;
  roles: Map<string, Set<string>>;
}

const CACHE_MS = 10_000;

@Injectable()
export class PrismaModelResolver implements GatewayModelResolver {
  private readonly logger = new Logger(PrismaModelResolver.name);
  private readonly cache = new TtlCache<ModelWorld>(CACHE_MS);

  constructor(private readonly prisma: PrismaService) {}

  async resolve(requested: string, key: GatewayKeyRecord): Promise<ResolvedGatewayModel> {
    const world = await this.world();
    const modelId = world.aliases.get(requested) ?? requested;
    const policy = world.policies.get(modelId);
    const known = world.catalogIds.has(modelId) || world.roles.has(modelId) || policy !== undefined;
    if (!known || policy?.enabled === false) throw new GatewayError('LAI-GW-020', { param: 'model' });
    if (!keyAllowsModel(key.allowedModels, requested, modelId)) throw new GatewayError('LAI-GW-012', { param: 'model' });
    const roles = world.roles.get(modelId);
    return {
      modelId,
      requestedModel: requested,
      policy: policy ?? DEFAULT_MODEL_POLICY,
      // A model whose nodes report no roles at all counts as "unknown", not as "has none".
      roles: roles && roles.size > 0 ? [...roles] : null,
      inApprovedCatalog: world.catalogIds.has(modelId),
    };
  }

  private async world(): Promise<ModelWorld> {
    const cached = this.cache.get('world');
    if (cached) return cached;
    try {
      const [policyRows, nodeModels, approved] = await Promise.all([
        this.prisma.localaiModelPolicy.findMany(),
        this.prisma.localaiNodeModel.findMany({ select: { modelId: true, roles: true } }),
        this.prisma.localaiCatalogVersion.findFirst({ where: { status: 'approved' }, select: { files: true } }),
      ]);
      const world: ModelWorld = { policies: new Map(), aliases: new Map(), catalogIds: new Set(Object.keys(modelSizesFromFiles(approved?.files))), roles: new Map() };
      for (const row of policyRows) {
        world.policies.set(row.modelId, {
          enabled: row.enabled,
          preferredNodes: row.preferredNodes,
          onDemandLoad: row.onDemandLoad,
          loadWaitS: row.loadWaitS,
          firstByteTimeoutS: row.firstByteTimeoutS,
        });
        for (const alias of row.aliases) world.aliases.set(alias, row.modelId);
      }
      for (const row of nodeModels) {
        const set = world.roles.get(row.modelId) ?? new Set<string>();
        for (const role of row.roles) set.add(role);
        world.roles.set(row.modelId, set);
      }
      this.cache.set('world', world);
      return world;
    } catch (error) {
      this.logger.warn(`could not read the model policies: ${error instanceof Error ? error.name : 'unknown error'}`);
      throw new GatewayError('LAI-GW-052');
    }
  }
}
