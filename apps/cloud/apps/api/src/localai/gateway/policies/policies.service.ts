// Model policies (docs/specs/gateway.md sections 3.6 and 3.8): the list for the Models page and the policy upsert.
//
// LIST. Every catalog model with its policy and live state, from the same inputs as `/v1/models`. An administrator sees
// all of them (disabled and unreachable ones too); anyone else sees what `/v1/models` lists.
//
// UPSERT. The model must be in the APPROVED catalog (404). An alias must not equal any catalog id (400) and must be unique
// across all policies (409): the check runs inside the store's transaction, behind an advisory lock, so two concurrent
// writes cannot both claim one alias. After the commit: the cached directory is dropped, `localai.gateway.policy.updated`
// is published and `localai.gateway.policy_updated` is audited; a failure of either is logged (the error name only) and
// never undoes the write.
import { BadRequestException, ConflictException, Inject, Injectable, Logger, NotFoundException } from '@nestjs/common';
import { AUDIT_SINK, type AuditSink } from '@marinoscar/platform-api/core';
import { EVENT_BUS, type EventBus } from '@marinoscar/platform-api/host';

import { AUDIT_GATEWAY_POLICY_UPDATED, AUDIT_TARGET_MODEL_POLICY } from '../gateway.audit';
import { isGatewayAdmin, type KeyActor } from '../keys/key-rules';
import type { ModelPolicy } from '../models/policy';
import { buildDirectory, ModelDirectoryProvider } from '../models/resolve';
import { RoutingTable } from '../routing/routing-table';
import { buildModelRow, buildModelRows, type PolicyMeta } from './model-rows';
import { GATEWAY_POLICY_UPDATED_CHANNEL, type GatewayPolicyUpdatedEvent } from './policy-events';
import { GATEWAY_POLICY_STORE, type PolicyData, type PolicyRecord, type PolicyStore } from './policy.store';
import type { ModelRowDto, PutPolicyBody } from './policies.schemas';

type AuditMeta = Record<string, string | number | boolean | null>;

/** The names of the fields of a policy that differ between two rows (`created` when there was none). */
export function changedFields(before: PolicyData | null, after: PolicyData): string[] {
  if (before === null) return ['created'];
  const changed: string[] = [];
  if (before.enabled !== after.enabled) changed.push('enabled');
  if (before.aliases.join('\n') !== after.aliases.join('\n')) changed.push('aliases');
  if (before.onDemandLoad !== after.onDemandLoad) changed.push('onDemandLoad');
  if (before.loadWaitS !== after.loadWaitS) changed.push('loadWaitS');
  if (before.firstByteTimeoutS !== after.firstByteTimeoutS) changed.push('firstByteTimeoutS');
  if (before.preferredNodes.join('\n') !== after.preferredNodes.join('\n')) changed.push('preferredNodes');
  return changed;
}

@Injectable()
export class PoliciesService {
  private readonly logger = new Logger(PoliciesService.name);

  constructor(
    @Inject(GATEWAY_POLICY_STORE) private readonly store: PolicyStore,
    private readonly directory: ModelDirectoryProvider,
    private readonly table: RoutingTable,
    @Inject(AUDIT_SINK) private readonly audit: AuditSink,
    @Inject(EVENT_BUS) private readonly bus: EventBus,
  ) {}

  async list(actor: KeyActor): Promise<ModelRowDto[]> {
    const [directory, stored] = await Promise.all([this.directory.load(), this.store.list()]);
    const meta = metaOf(stored);
    return buildModelRows(directory, (modelId) => this.table.healthyCandidates(modelId), (modelId) => meta.get(modelId), isGatewayAdmin(actor));
  }

  async upsert(actor: KeyActor, modelId: string, body: PutPolicyBody): Promise<ModelRowDto> {
    const directory = await this.directory.load();
    const catalog = directory.catalog;
    if (!catalog || !catalog.models.has(modelId)) {
      throw new NotFoundException({ message: 'No such model in the approved catalog', details: { reason: 'unknown_model' } });
    }

    const clash = body.aliases.findIndex((alias) => catalog.models.has(alias));
    if (clash !== -1) {
      throw new BadRequestException({
        message: `The alias ${body.aliases[clash]} is the id of a catalog model`,
        details: { issues: [{ path: `aliases.${clash}`, message: 'An alias cannot be the id of a catalog model' }] },
      });
    }

    const data: PolicyData = {
      enabled: body.enabled,
      aliases: body.aliases,
      onDemandLoad: body.onDemandLoad,
      loadWaitS: body.loadWaitS,
      firstByteTimeoutS: body.firstByteTimeoutS,
      preferredNodes: body.preferredNodes,
    };
    const { before, after } = await this.store.upsert(modelId, data, actor.userId, (others) => {
      for (const alias of body.aliases) {
        // A policy row's model id is a catalog id too, even if the catalog dropped the model since.
        if (others.some((row) => row.modelId === alias)) {
          throw new BadRequestException({ message: `The alias ${alias} is the id of a model`, details: { issues: [{ path: 'aliases', message: 'An alias cannot be the id of a model' }] } });
        }
        const owner = others.find((row) => row.aliases.includes(alias));
        if (owner) {
          throw new ConflictException({ message: `The alias ${alias} already belongs to ${owner.modelId}`, details: { reason: 'alias_taken', alias, modelId: owner.modelId } });
        }
      }
    });

    await this.afterCommit(actor, modelId, before, after);
    // Answer from the row just written, not from a re-read of the (cached) directory.
    const fresh = await this.directory.load();
    const written: ModelPolicy = {
      enabled: after.enabled,
      aliases: after.aliases,
      onDemandLoad: after.onDemandLoad,
      loadWaitS: after.loadWaitS,
      firstByteTimeoutS: after.firstByteTimeoutS,
      preferredNodes: after.preferredNodes,
    };
    const current = buildDirectory(fresh.catalog ?? catalog, new Map(fresh.policies).set(modelId, written));
    return buildModelRow(current, modelId, (id) => this.table.healthyCandidates(id), (id) => (id === modelId ? { updatedAt: after.updatedAt } : undefined))!;
  }

  private async afterCommit(actor: KeyActor, modelId: string, before: PolicyRecord | null, after: PolicyRecord): Promise<void> {
    this.directory.invalidate();
    try {
      await this.bus.publish<GatewayPolicyUpdatedEvent>(GATEWAY_POLICY_UPDATED_CHANNEL, { modelId });
    } catch (error) {
      this.logger.warn(`could not publish ${GATEWAY_POLICY_UPDATED_CHANNEL} (${errorName(error)}); the caches expire on their own within 5 s`);
    }
    const changed = changedFields(before, after);
    if (changed.length === 0) return;
    const meta: AuditMeta = {
      changed: changed.join(','),
      enabled: after.enabled,
      aliases: after.aliases.join(',') || 'none',
      onDemandLoad: after.onDemandLoad,
      loadWaitS: after.loadWaitS,
      firstByteTimeoutS: after.firstByteTimeoutS,
      preferredNodes: after.preferredNodes.join(',') || 'none',
    };
    try {
      await this.audit.record({ action: AUDIT_GATEWAY_POLICY_UPDATED, actorUserId: actor.userId, targetType: AUDIT_TARGET_MODEL_POLICY, targetId: modelId, meta });
    } catch (error) {
      this.logger.error(`recording ${AUDIT_GATEWAY_POLICY_UPDATED} for ${modelId} failed (${errorName(error)})`);
    }
  }
}

function metaOf(rows: readonly PolicyRecord[]): Map<string, PolicyMeta> {
  return new Map(rows.map((row) => [row.modelId, { updatedAt: row.updatedAt }]));
}

function errorName(error: unknown): string {
  return error instanceof Error ? error.name : 'error';
}
