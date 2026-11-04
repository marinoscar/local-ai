// The alert engine (docs/specs/cloud-app.md section 4.9): evaluate the rules for every node (or one), reconcile the
// result with the active rows, announce what changed, and serve the two UI routes' logic (list, acknowledge).
//
// RECONCILE, per node:  desired = rules(node, now, active)
//   in desired, not active   -> INSERT (the partial unique index decides the winner under concurrency)
//   in both, same severity   -> refresh `lastSeenAt` (and the title, which carries a changing age)
//   in both, severity moved  -> change it; a RISE forgets the ack and the last notice so it is announced again
//   active, not desired      -> clear (only evaluator-owned kinds: `endpoint.down` is the gateway probe's)
// A node that is retired has every active alert cleared. A retired node is never read by the rules.
//
// NOTIFY. Only the call whose conditional write won announces a change, and the announcement happens AFTER that write
// committed, outside any transaction (nothing here opens one). A new alert, an escalation and an unacknowledged repeat
// all flow through ONE claim, `claimNotice` (set `notifiedAt` while it is still due), so exactly one dispatch happens
// per due alert however many evaluations race. Refreshing a still-true alert never notifies on its own.
//
// Nothing here is slow: a handful of indexed statements per node. It still runs in a queue job (`alerts-evaluate.job.ts`),
// never inline in a cron or a request (docs/specs/job-queue.md).
import { ConflictException, Inject, Injectable, Logger, NotFoundException } from '@nestjs/common';
import { AUDIT_SINK, type AuditSink } from '@marinoscar/platform-api/core';
import { EVENT_BUS, type EventBus } from '@marinoscar/platform-api/host';

import { LOCALAI_CLOCK, type LocalaiClock } from '../commands/command-events';
import {
  ALERT_CHANGED_CHANNEL,
  LOCALAI_ALERT_NOTIFIER,
  type AlertChangedEvent,
  type AlertNotice,
  type AlertNotifier,
} from './alert-ports';
import { SEVERITY_RANK, isEvaluatorOwnedKind, isExternalKind, plainFix, type AlertSeverity } from './alert-kinds';
import { evaluateNodeAlerts, type ActiveAlertView, type DesiredAlert, type RuleContext } from './alert-rules';
import { AlertSettingsReader } from './alert-settings';
import { ALERT_STORE, type AlertListFilter, type AlertRecord, type AlertStore, type EvaluationNode } from './alert-store';

/** Audit action (permanent string, spec section 3.5). */
export const AUDIT_ALERT_ACKNOWLEDGED = 'localai.alert.acknowledged';

const HOUR_MS = 3_600_000;

export interface EvaluationSummary {
  nodes: number;
  raised: number;
  escalated: number;
  cleared: number;
  notified: number;
}

const empty = (): EvaluationSummary => ({ nodes: 0, raised: 0, escalated: 0, cleared: 0, notified: 0 });

export interface ExternalAlertInput {
  code: string;
  severity: AlertSeverity;
  title: string;
  details?: Record<string, unknown>;
}

const view = (record: AlertRecord): ActiveAlertView => ({ kind: record.kind, code: record.code, severity: record.severity, title: record.title, details: record.details });

@Injectable()
export class AlertsService {
  private readonly logger = new Logger(AlertsService.name);

  constructor(
    @Inject(ALERT_STORE) private readonly store: AlertStore,
    private readonly settings: AlertSettingsReader,
    @Inject(LOCALAI_ALERT_NOTIFIER) private readonly notifier: AlertNotifier,
    @Inject(EVENT_BUS) private readonly bus: EventBus,
    @Inject(AUDIT_SINK) private readonly audit: AuditSink,
    @Inject(LOCALAI_CLOCK) private readonly clock: LocalaiClock,
  ) {}

  /** Evaluates every non-retired node, clears the alerts of retired ones, then sends what is due. */
  async evaluateAll(): Promise<EvaluationSummary> {
    const now = this.clock.now();
    const summary = empty();
    const context = await this.context(now);
    const nodes = await this.store.loadNodes();
    const activeByNode = await this.activeByNode();
    for (const node of nodes) await this.reconcile(node, context, activeByNode.get(node.rowId) ?? [], summary);
    for (const record of await this.store.listActiveOfRetiredNodes()) await this.clear(record, now, summary);
    summary.nodes = nodes.length;
    summary.notified = await this.sendDue(context, now);
    return summary;
  }

  /** Evaluates one node by its lab id (a mode change). An unknown or retired node is a no-op. */
  async evaluateNode(labNodeId: string): Promise<EvaluationSummary> {
    const now = this.clock.now();
    const summary = empty();
    const context = await this.context(now);
    const [node] = await this.store.loadNodes(labNodeId);
    if (node) {
      const active = (await this.store.listActive({ nodeRowId: node.rowId })).filter((record) => isEvaluatorOwnedKind(record.kind));
      await this.reconcile(node, context, active, summary);
      summary.nodes = 1;
    }
    summary.notified = await this.sendDue(context, now);
    return summary;
  }

  /**
   * Raises (or refreshes) an alert whose truth is owned elsewhere, for example `endpoint.down` from the gateway probe.
   * Returns true when this call created the alert. The evaluator never clears it; `clearExternal` does.
   */
  async raiseExternal(labNodeId: string, kind: string, input: ExternalAlertInput): Promise<boolean> {
    if (!isExternalKind(kind)) throw new Error(`raiseExternal: '${kind}' is not an externally owned alert kind`);
    const now = this.clock.now();
    const [node] = await this.store.loadNodes(labNodeId);
    if (!node) return false;
    const details = input.details ?? {};
    const active = (await this.store.listActive({ nodeRowId: node.rowId })).find((record) => record.kind === kind);
    let created = false;
    if (!active) {
      const inserted = await this.store.insert({ nodeRowId: node.rowId, kind, code: input.code, severity: input.severity, title: input.title, details }, now);
      if (inserted) {
        created = true;
        await this.publish(inserted, 'active');
      }
    } else if (active.severity === input.severity) {
      await this.store.refresh(active.id, { title: input.title, details }, now);
    } else {
      const rise = SEVERITY_RANK[input.severity] > SEVERITY_RANK[active.severity];
      if (await this.store.changeSeverity(active.id, { from: active.severity, to: input.severity, title: input.title, details, resetNotice: rise }, now)) {
        await this.publish({ ...active, severity: input.severity }, 'active');
      }
    }
    await this.sendDue(await this.context(now), now);
    return created;
  }

  /** Clears an externally owned alert. Returns true when this call cleared it. */
  async clearExternal(labNodeId: string, kind: string): Promise<boolean> {
    if (!isExternalKind(kind)) throw new Error(`clearExternal: '${kind}' is not an externally owned alert kind`);
    const now = this.clock.now();
    const [node] = await this.store.loadNodes(labNodeId);
    if (!node) return false;
    const active = (await this.store.listActive({ nodeRowId: node.rowId })).find((record) => record.kind === kind);
    if (!active) return false;
    const summary = empty();
    await this.clear(active, now, summary);
    return summary.cleared === 1;
  }

  /** `GET /api/localai/alerts`: state, node filter and a bound on the count. Active alerts come most urgent first. */
  async list(filter: AlertListFilter): Promise<AlertRecord[]> {
    const records = await this.store.list(filter);
    if (filter.state === 'cleared') return records;
    return [...records].sort(
      (a, b) =>
        Number(b.state === 'active') - Number(a.state === 'active') ||
        SEVERITY_RANK[b.severity] - SEVERITY_RANK[a.severity] ||
        b.firstRaisedAt.getTime() - a.firstRaisedAt.getTime(),
    );
  }

  /**
   * `POST /api/localai/alerts/:id/ack`: sets `acknowledgedAt/ById`, which stops the repeat notifications; it does NOT
   * clear the alert. Acknowledging twice is a success and keeps the first acknowledgement. A cleared alert is a 409.
   */
  async acknowledge(id: string, userId: string): Promise<AlertRecord> {
    const record = await this.store.get(id);
    if (!record) throw new NotFoundException({ message: 'No alert has this id' });
    if (record.state !== 'active') throw new ConflictException({ message: 'This alert is already resolved.' });
    const won = await this.store.acknowledge(id, userId, this.clock.now());
    if (won) {
      try {
        await this.audit.record({
          action: AUDIT_ALERT_ACKNOWLEDGED,
          actorUserId: userId,
          targetType: 'localai_alert',
          targetId: id,
          meta: { kind: record.kind, nodeId: record.nodeId },
        });
      } catch (error) {
        this.logger.error(`audit ${AUDIT_ALERT_ACKNOWLEDGED} failed for alert_id=${id}: ${error instanceof Error ? error.name : 'unknown error'}`);
      }
    }
    return (await this.store.get(id)) ?? record;
  }

  // -- internals ---------------------------------------------------------------------------------------------------

  private async context(now: Date): Promise<RuleContext & { repeatHours: number }> {
    const [settings, approvedCatalog] = await Promise.all([this.settings.read(), this.store.approvedCatalog()]);
    return { now, settings, approvedCatalog, repeatHours: settings.repeatHours };
  }

  private async activeByNode(): Promise<Map<string, AlertRecord[]>> {
    const byNode = new Map<string, AlertRecord[]>();
    for (const record of await this.store.listActive({ all: true })) {
      if (record.nodeRowId === null || !isEvaluatorOwnedKind(record.kind)) continue;
      byNode.set(record.nodeRowId, [...(byNode.get(record.nodeRowId) ?? []), record]);
    }
    return byNode;
  }

  private async reconcile(node: EvaluationNode, context: RuleContext, active: AlertRecord[], summary: EvaluationSummary): Promise<void> {
    try {
      const byKind = new Map(active.map((record) => [record.kind, record]));
      const desired = evaluateNodeAlerts(node, context, new Map(active.map((record) => [record.kind, view(record)])));
      const wanted = new Set<string>();
      for (const alert of desired) {
        wanted.add(alert.kind);
        await this.apply(node, alert, byKind.get(alert.kind), context.now, summary);
      }
      for (const record of active) if (!wanted.has(record.kind)) await this.clear(record, context.now, summary);
    } catch (error) {
      // One node's failure must not stop the others; the next run (5 minutes) tries again.
      this.logger.error(`alert evaluation failed for node_id=${node.nodeId}: ${error instanceof Error ? error.name : 'unknown error'}`);
    }
  }

  private async apply(node: EvaluationNode, alert: DesiredAlert, existing: AlertRecord | undefined, now: Date, summary: EvaluationSummary): Promise<void> {
    if (!existing) {
      const inserted = await this.store.insert({ nodeRowId: node.rowId, kind: alert.kind, code: alert.code, severity: alert.severity, title: alert.title, details: alert.details }, now);
      // null: a concurrent evaluation inserted it first; it announces it.
      if (inserted) {
        summary.raised += 1;
        await this.publish(inserted, 'active');
      }
      return;
    }
    if (existing.severity === alert.severity) {
      await this.store.refresh(existing.id, { title: alert.title, details: alert.details }, now);
      return;
    }
    const rise = SEVERITY_RANK[alert.severity] > SEVERITY_RANK[existing.severity];
    const changed = await this.store.changeSeverity(
      existing.id,
      { from: existing.severity, to: alert.severity, title: alert.title, details: alert.details, resetNotice: rise },
      now,
    );
    if (changed) {
      if (rise) summary.escalated += 1;
      await this.publish({ ...existing, severity: alert.severity }, 'active');
    }
  }

  private async clear(record: AlertRecord, now: Date, summary: EvaluationSummary): Promise<void> {
    if (!(await this.store.clear(record.id, now))) return;
    summary.cleared += 1;
    await this.publish(record, 'cleared');
    // Only an alert somebody was told about is announced as resolved.
    if (record.notifiedAt !== null) await this.safely(() => this.notifier.cleared(this.notice(record)), 'cleared', record);
  }

  /** Sends every notification that is due, each claimed first so a concurrent run cannot send it twice. */
  private async sendDue(context: { now: Date; repeatHours: number }, now: Date): Promise<number> {
    const repeatBefore = new Date(now.getTime() - context.repeatHours * HOUR_MS);
    let sent = 0;
    for (const record of await this.store.listDueForNotice(repeatBefore)) {
      if (!(await this.store.claimNotice(record.id, now, repeatBefore))) continue;
      sent += 1;
      await this.safely(() => this.notifier.raised(this.notice(record)), 'raised', record);
    }
    return sent;
  }

  private notice(record: AlertRecord): AlertNotice {
    return {
      alertId: record.id,
      node: record.nodeId ?? 'Lab',
      kind: record.kind,
      code: record.code,
      severity: record.severity,
      title: record.title,
      fix: plainFix(record.code),
    };
  }

  private async publish(record: AlertRecord, state: AlertChangedEvent['state']): Promise<void> {
    const event: AlertChangedEvent = { alertId: record.id, nodeId: record.nodeId, kind: record.kind, state };
    try {
      await this.bus.publish(ALERT_CHANGED_CHANNEL, event);
    } catch (error) {
      this.logger.error(`publish ${ALERT_CHANGED_CHANNEL} failed for alert_id=${record.id}: ${error instanceof Error ? error.name : 'unknown error'}`);
    }
  }

  private async safely(send: () => Promise<void>, what: string, record: AlertRecord): Promise<void> {
    try {
      await send();
    } catch (error) {
      this.logger.error(`notification ${what} failed for alert_id=${record.id}: ${error instanceof Error ? error.name : 'unknown error'}`);
    }
  }
}
