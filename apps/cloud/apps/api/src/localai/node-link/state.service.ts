// State ingestion (docs/specs/cloud-app.md sections 3.6 and 4.3): validate, scrub, order, store verbatim, project
// `models[]` into `localai_node_models`, then announce.
//
// ORDER OF CHECKS (cheapest and safest first, every refusal before any write):
//   1. scrub the RAW body (a secret-looking string is refused before Zod can echo it in an issue);
//   2. parse with the strict `LocalaiStateSnapshotV1`;
//   3. `node_id` must be the bound node (400 LAI-CLD-004);
//   4. in ONE transaction: lock the node row, check the order, store, project.
// After the commit (outside any transaction): publish `localai.node.state` once, and when the mode changed audit
// `localai.node.mode_changed` and call the alert re-evaluation hook. Those can fail without undoing the snapshot.
//
// SECRETS. The body is never logged, never put in an error, never echoed. A refusal logs the node id, the JSON PATH
// of the offending value and the rule name; a log line about an accepted snapshot has the node id, boot-relative seq
// and counts.
import { Inject, Injectable, Logger } from '@nestjs/common';
import { AUDIT_SINK, type AuditSink } from '@marinoscar/platform-api/core';
import { EVENT_BUS, type EventBus } from '@marinoscar/platform-api/host';
import { Prisma, type LocalaiNode } from '@prisma/client';

import { PrismaService } from '../../prisma/prisma.service';
import { scrubSecrets, ScrubLimitError } from '../contracts/scrubber';
import { LocalaiStateSnapshotV1, type LocalaiStateResponse } from '../contracts/state.schema';
import { bindingRefusal, LAI_CLD_NOT_ENROLLED, LAI_CLD_WRONG_NODE } from './node-link.errors';
import { LOCALAI_ALERT_REEVALUATOR, type LocalaiAlertReevaluator } from './state.hooks';
import { nodeIdMismatch, safeIssues, schemaRefusal, secretRefusal, staleSeq } from './state.errors';
import { carryOverReady, diffModels, hasModeChanged, isFreshSnapshot, type ModelRow } from './state.projection';

/** The event bus channel (spec section 7). The payload is `{ nodeId, modelsChanged, modeChanged }`, far below 7.5 kB. */
export const LOCALAI_NODE_STATE_CHANNEL = 'localai.node.state';
/** Audit action (permanent string, spec section 3.5). */
export const AUDIT_NODE_MODE_CHANGED = 'localai.node.mode_changed';

export interface LocalaiNodeStateEvent {
  /** The lab node id, `ai-n<N>`. */
  nodeId: string;
  /** A `localai_node_models` row was created, changed or deleted. */
  modelsChanged: boolean;
  /** The node's mode differs from the previous one (the first snapshot is not a change). */
  modeChanged: boolean;
}

@Injectable()
export class StateService {
  private readonly logger = new Logger(StateService.name);

  constructor(
    private readonly prisma: PrismaService,
    @Inject(AUDIT_SINK) private readonly audit: AuditSink,
    @Inject(EVENT_BUS) private readonly bus: EventBus,
    @Inject(LOCALAI_ALERT_REEVALUATOR) private readonly alerts: LocalaiAlertReevaluator,
  ) {}

  /**
   * Ingests one snapshot for the verified node. `actorUserId` is the owner of the node credential (the audit actor,
   * as for the binding guard). Throws EAB-format HTTP exceptions for every refusal.
   */
  async ingest(node: LocalaiNode, actorUserId: string | null, body: unknown): Promise<LocalaiStateResponse> {
    const snapshot = this.validate(node, body);

    const outcome = await this.prisma.$transaction(async (tx) => {
      // Lock the row: an UPDATE takes the row lock and a concurrent snapshot of the same node waits here until this
      // transaction ends, so the order check below and the write after it are one step (READ COMMITTED re-reads the
      // committed row after the wait). A touch of `updated_at` rather than `SELECT ... FOR UPDATE`: no raw SQL.
      const locked = await tx.localaiNode.updateMany({ where: { id: node.id }, data: { updatedAt: new Date() } });
      if (locked.count === 0) throw bindingRefusal(LAI_CLD_WRONG_NODE, 'unknown_localai_node');
      const row = await tx.localaiNode.findUnique({
        where: { id: node.id },
        select: { phase: true, mode: true, lastState: true, lastStateSeq: true },
      });
      if (!row) throw bindingRefusal(LAI_CLD_WRONG_NODE, 'unknown_localai_node');
      if (row.phase === 'retired') throw bindingRefusal(LAI_CLD_NOT_ENROLLED, 'node_retired');
      if (!isFreshSnapshot({ lastState: row.lastState, lastStateSeq: row.lastStateSeq }, snapshot)) throw staleSeq();

      const toStore = carryOverReady(row.lastState, snapshot);
      await tx.localaiNode.update({
        where: { id: node.id },
        data: {
          lastState: toStore as unknown as Prisma.InputJsonValue,
          lastStateAt: new Date(),
          lastStateSeq: BigInt(snapshot.seq),
          mode: snapshot.mode,
          connectivity: snapshot.connectivity,
          profile: snapshot.profile,
          agentVersion: snapshot.agent_version,
          catalogVersion: snapshot.catalog_version,
          backend: snapshot.llama.backend,
        },
      });

      const existing: ModelRow[] = await tx.localaiNodeModel.findMany({
        where: { nodeId: node.id },
        select: {
          modelId: true,
          present: true,
          verified: true,
          loadable: true,
          loadableReason: true,
          status: true,
          isDefault: true,
          slots: true,
          roles: true,
          inputModalities: true,
          ctx: true,
        },
      });
      const diff = diffModels(existing, snapshot.models);
      if (diff.remove.length > 0) await tx.localaiNodeModel.deleteMany({ where: { nodeId: node.id, modelId: { in: diff.remove } } });
      if (diff.create.length > 0) {
        await tx.localaiNodeModel.createMany({ data: diff.create.map((model) => ({ nodeId: node.id, ...model })) });
      }
      for (const model of diff.update) {
        const { modelId, ...fields } = model;
        await tx.localaiNodeModel.update({ where: { nodeId_modelId: { nodeId: node.id, modelId } }, data: fields });
      }
      return { modelsChanged: diff.changed, previousMode: row.mode };
    });

    const modeChanged = hasModeChanged(outcome.previousMode, snapshot.mode);
    await this.announce(node, actorUserId, {
      event: { nodeId: node.nodeId, modelsChanged: outcome.modelsChanged, modeChanged },
      from: outcome.previousMode,
      to: snapshot.mode,
    });
    this.logger.debug(
      `state accepted: node_id=${node.nodeId} seq=${snapshot.seq} models=${snapshot.models.length} models_changed=${outcome.modelsChanged} mode_changed=${modeChanged}`,
    );
    return { accepted: true, seq: snapshot.seq };
  }

  /** Scrub, parse, and bind to the node. Throws the 400 refusals; logs paths and rules only. */
  private validate(node: LocalaiNode, body: unknown): LocalaiStateSnapshotV1 {
    try {
      const finding = scrubSecrets(body);
      if (finding) {
        this.logger.warn(`state refused: node_id=${node.nodeId} reason=secret_like_value rule=${finding.rule} path=${finding.path}`);
        throw secretRefusal(finding.path);
      }
    } catch (error) {
      if (error instanceof ScrubLimitError) {
        this.logger.warn(`state refused: node_id=${node.nodeId} reason=schema limit=${error.limit}`);
        throw schemaRefusal([{ path: '$', code: 'too_large' }]);
      }
      throw error;
    }

    const parsed = LocalaiStateSnapshotV1.safeParse(body);
    if (!parsed.success) {
      const issues = safeIssues(parsed.error);
      this.logger.warn(`state refused: node_id=${node.nodeId} reason=schema paths=${issues.map((issue) => `${issue.path}:${issue.code}`).join(',')}`);
      throw schemaRefusal(issues);
    }
    if (parsed.data.node_id !== node.nodeId) {
      this.logger.warn(`state refused: node_id=${node.nodeId} reason=node_id_mismatch`);
      throw nodeIdMismatch();
    }
    return parsed.data;
  }

  /** After the commit: publish once, then, on a mode change, audit and re-evaluate alerts. None of it can refuse. */
  private async announce(
    node: LocalaiNode,
    actorUserId: string | null,
    change: { event: LocalaiNodeStateEvent; from: string | null; to: string },
  ): Promise<void> {
    try {
      await this.bus.publish(LOCALAI_NODE_STATE_CHANNEL, change.event);
    } catch (error) {
      this.logger.error(`publish ${LOCALAI_NODE_STATE_CHANNEL} failed for node_id=${node.nodeId}: ${describe(error)}`);
    }
    if (!change.event.modeChanged) return;
    try {
      await this.audit.record({
        action: AUDIT_NODE_MODE_CHANGED,
        actorUserId,
        targetType: 'localai_node',
        targetId: node.nodeId,
        meta: { from: change.from, to: change.to },
      });
    } catch (error) {
      this.logger.error(`audit ${AUDIT_NODE_MODE_CHANGED} failed for node_id=${node.nodeId}: ${describe(error)}`);
    }
    try {
      await this.alerts.reevaluateNode(node.nodeId);
    } catch (error) {
      this.logger.error(`alert re-evaluation failed for node_id=${node.nodeId}: ${describe(error)}`);
    }
  }
}

/** Error text only: never the error object, whose message could in principle embed request data. */
function describe(error: unknown): string {
  return error instanceof Error ? error.name : 'unknown error';
}
