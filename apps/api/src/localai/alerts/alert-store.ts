// The storage port of the alert engine. `AlertsService` holds every decision (what to raise, refresh, escalate, clear,
// notify); this is the only thing it needs from the database, so the whole engine is table-tested against an in-memory
// fake (`alerts.service.spec.ts`) and proven against PostgreSQL in `test/localai/alerts.db.spec.ts`.
//
// EVERY WRITE IS CONDITIONAL AND REPORTS WHETHER IT WON. Two evaluations can run at once (the cron job and a mode
// change), so "I changed the row" must come from the database, not from a read before the write: only the caller whose
// write returns true announces the change. `insert` leans on the partial unique index `localai_alerts_one_active`
// (`ON CONFLICT DO NOTHING`, never a `findFirst` pre-check).
import type { AlertSeverity, AlertState } from './alert-kinds';
import type { RuleNode } from './alert-rules';

/** One alert row as the engine and the API see it. */
export interface AlertRecord {
  id: string;
  /** `localai_nodes.id` (the uuid), or null for a lab-wide alert. */
  nodeRowId: string | null;
  /** The lab node id, `ai-n<N>`, or null for a lab-wide alert. */
  nodeId: string | null;
  nodeName: string | null;
  kind: string;
  code: string;
  severity: AlertSeverity;
  state: AlertState;
  title: string;
  details: Record<string, unknown> | null;
  firstRaisedAt: Date;
  lastSeenAt: Date;
  clearedAt: Date | null;
  acknowledgedAt: Date | null;
  notifiedAt: Date | null;
}

/** A node as the evaluator reads it: the rule input plus the row id the alerts hang on. */
export interface EvaluationNode extends RuleNode {
  /** `localai_nodes.id`. */
  rowId: string;
  displayName: string;
}

export interface NewAlert {
  nodeRowId: string | null;
  kind: string;
  code: string;
  severity: AlertSeverity;
  title: string;
  details: Record<string, unknown>;
}

export interface AlertListFilter {
  state: AlertState | 'all';
  /** The lab node id (`ai-n3`) to filter on. */
  nodeId?: string;
  limit: number;
}

export interface AlertStore {
  /** Non-retired nodes (enrolled and pending), or the one with this lab id. Empty when the id is unknown or retired. */
  loadNodes(labNodeId?: string): Promise<EvaluationNode[]>;
  /** The approved catalog version and when it was approved, or null when none is approved. */
  approvedCatalog(): Promise<{ version: string; approvedAt: Date | null } | null>;
  /** Active alerts: of one node (row id), of the whole lab, or (null) of the lab-wide ones. */
  listActive(scope: { nodeRowId: string } | { all: true }): Promise<AlertRecord[]>;
  /** Active alerts that belong to a node that is now retired. */
  listActiveOfRetiredNodes(): Promise<AlertRecord[]>;
  /** Inserts an active alert; null when one is already active for the same (node, kind) (the unique index said no). */
  insert(alert: NewAlert, now: Date): Promise<AlertRecord | null>;
  /** Same severity: refreshes `lastSeenAt`, the title and the details. True when the row was still active. */
  refresh(id: string, patch: { title: string; details: Record<string, unknown> }, now: Date): Promise<boolean>;
  /**
   * Moves an active alert from `from` to `to`. An ESCALATION (`resetNotice`) also forgets the acknowledgement and the
   * last notification, so the louder severity is announced and not swallowed by an old ack. True when this call did it.
   */
  changeSeverity(id: string, change: { from: AlertSeverity; to: AlertSeverity; title: string; details: Record<string, unknown>; resetNotice: boolean }, now: Date): Promise<boolean>;
  /** Clears an active alert. True when THIS call cleared it (so only one of two concurrent evaluations announces it). */
  clear(id: string, now: Date): Promise<boolean>;
  /** Active alerts due a notification: never notified, or unacknowledged and last notified before `repeatBefore`. */
  listDueForNotice(repeatBefore: Date): Promise<AlertRecord[]>;
  /** Claims the notification of `id`: sets `notifiedAt` only while it is still due. True when this call claimed it. */
  claimNotice(id: string, now: Date, repeatBefore: Date): Promise<boolean>;
  get(id: string): Promise<AlertRecord | null>;
  list(filter: AlertListFilter): Promise<AlertRecord[]>;
  /** Sets `acknowledgedAt/ById` on an active, unacknowledged alert. True when this call acknowledged it. */
  acknowledge(id: string, userId: string, now: Date): Promise<boolean>;
}

/** Injection token of the {@link AlertStore}. */
export const ALERT_STORE = Symbol('LOCALAI_ALERT_STORE');
