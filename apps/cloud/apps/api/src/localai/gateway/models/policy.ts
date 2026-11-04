// Per-model routing policy as the gateway reads it (`localai_model_policies`, docs/specs/gateway.md section 3.6). No
// row means enabled, no aliases, no on-demand load. The policy CRUD and page are GW-09; this is the read side that the
// listing, the model resolution and (later) the on-demand path share.
import { Injectable } from '@nestjs/common';

import { PrismaService } from '../../../prisma/prisma.service';

export interface ModelPolicy {
  enabled: boolean;
  aliases: readonly string[];
  onDemandLoad: boolean;
  /** Seconds a request waits for an on-demand load (10 to 600). */
  loadWaitS: number;
  firstByteTimeoutS: number | null;
  /** Node ids in order of preference. */
  preferredNodes: readonly string[];
}

/** What a model without a policy row gets (the header of the data model: enabled, no on-demand load). */
export const DEFAULT_MODEL_POLICY: ModelPolicy = {
  enabled: true,
  aliases: [],
  onDemandLoad: false,
  loadWaitS: 120,
  firstByteTimeoutS: null,
  preferredNodes: [],
};

export interface ModelPolicySource {
  /** Every stored policy by catalog model id. */
  all(): Promise<Map<string, ModelPolicy>>;
}

/** Injection token of the {@link ModelPolicySource}. */
export const MODEL_POLICY_SOURCE = Symbol('LOCALAI_GATEWAY_MODEL_POLICY_SOURCE');

@Injectable()
export class PrismaModelPolicySource implements ModelPolicySource {
  constructor(private readonly prisma: PrismaService) {}

  async all(): Promise<Map<string, ModelPolicy>> {
    const rows = await this.prisma.localaiModelPolicy.findMany();
    return new Map(
      rows.map((row): [string, ModelPolicy] => [
        row.modelId,
        {
          enabled: row.enabled,
          aliases: row.aliases,
          onDemandLoad: row.onDemandLoad,
          loadWaitS: row.loadWaitS,
          firstByteTimeoutS: row.firstByteTimeoutS,
          preferredNodes: row.preferredNodes,
        },
      ]),
    );
  }
}
