// An in-memory `AlertStore` for the engine's unit tests (not production code). It enforces the same rule the database
// does: at most one ACTIVE alert per (node, kind) (`insert` returns null on a clash), and every write is conditional.
import type { AlertSeverity } from './alert-kinds';
import type { AlertListFilter, AlertRecord, AlertStore, EvaluationNode, NewAlert } from './alert-store';

export class FakeAlertStore implements AlertStore {
  nodes: EvaluationNode[] = [];
  retiredNodeRowIds = new Set<string>();
  catalog: { version: string; approvedAt: Date | null } | null = null;
  rows: AlertRecord[] = [];
  private seq = 0;
  /** Every `insert` call, including the ones the unique rule refused. */
  insertAttempts = 0;

  async loadNodes(labNodeId?: string): Promise<EvaluationNode[]> {
    return this.nodes.filter((node) => labNodeId === undefined || node.nodeId === labNodeId);
  }

  async approvedCatalog() {
    return this.catalog;
  }

  async listActive(scope: { nodeRowId: string } | { all: true }): Promise<AlertRecord[]> {
    return this.rows.filter((row) => row.state === 'active' && ('all' in scope || row.nodeRowId === scope.nodeRowId)).map((row) => ({ ...row }));
  }

  async listActiveOfRetiredNodes(): Promise<AlertRecord[]> {
    return this.rows.filter((row) => row.state === 'active' && row.nodeRowId !== null && this.retiredNodeRowIds.has(row.nodeRowId)).map((row) => ({ ...row }));
  }

  async insert(alert: NewAlert, now: Date): Promise<AlertRecord | null> {
    this.insertAttempts += 1;
    if (this.rows.some((row) => row.state === 'active' && row.nodeRowId === alert.nodeRowId && row.kind === alert.kind)) return null;
    const node = this.nodes.find((candidate) => candidate.rowId === alert.nodeRowId);
    this.seq += 1;
    const row: AlertRecord = {
      id: `alert-${this.seq}`,
      nodeRowId: alert.nodeRowId,
      nodeId: node?.nodeId ?? null,
      nodeName: node?.displayName ?? null,
      kind: alert.kind,
      code: alert.code,
      severity: alert.severity,
      state: 'active',
      title: alert.title,
      details: alert.details,
      firstRaisedAt: now,
      lastSeenAt: now,
      clearedAt: null,
      acknowledgedAt: null,
      notifiedAt: null,
    };
    this.rows.push(row);
    return { ...row };
  }

  private active(id: string): AlertRecord | undefined {
    return this.rows.find((row) => row.id === id && row.state === 'active');
  }

  async refresh(id: string, patch: { title: string; details: Record<string, unknown> }, now: Date): Promise<boolean> {
    const row = this.active(id);
    if (!row) return false;
    Object.assign(row, { title: patch.title, details: patch.details, lastSeenAt: now });
    return true;
  }

  async changeSeverity(
    id: string,
    change: { from: AlertSeverity; to: AlertSeverity; title: string; details: Record<string, unknown>; resetNotice: boolean },
    now: Date,
  ): Promise<boolean> {
    const row = this.active(id);
    if (!row || row.severity !== change.from) return false;
    Object.assign(row, { severity: change.to, title: change.title, details: change.details, lastSeenAt: now });
    if (change.resetNotice) Object.assign(row, { notifiedAt: null, acknowledgedAt: null });
    return true;
  }

  async clear(id: string, now: Date): Promise<boolean> {
    const row = this.active(id);
    if (!row) return false;
    Object.assign(row, { state: 'cleared', clearedAt: now });
    return true;
  }

  private due(row: AlertRecord, repeatBefore: Date): boolean {
    return row.state === 'active' && (row.notifiedAt === null || (row.acknowledgedAt === null && row.notifiedAt.getTime() < repeatBefore.getTime()));
  }

  async listDueForNotice(repeatBefore: Date): Promise<AlertRecord[]> {
    return this.rows.filter((row) => this.due(row, repeatBefore)).map((row) => ({ ...row }));
  }

  async claimNotice(id: string, now: Date, repeatBefore: Date): Promise<boolean> {
    const row = this.rows.find((candidate) => candidate.id === id);
    if (!row || !this.due(row, repeatBefore)) return false;
    row.notifiedAt = now;
    return true;
  }

  async get(id: string): Promise<AlertRecord | null> {
    const row = this.rows.find((candidate) => candidate.id === id);
    return row ? { ...row } : null;
  }

  async list(filter: AlertListFilter): Promise<AlertRecord[]> {
    return this.rows
      .filter((row) => (filter.state === 'all' || row.state === filter.state) && (filter.nodeId === undefined || row.nodeId === filter.nodeId))
      .slice(0, filter.limit)
      .map((row) => ({ ...row }));
  }

  async acknowledge(id: string, userId: string, now: Date): Promise<boolean> {
    const row = this.active(id);
    if (!row || row.acknowledgedAt !== null) return false;
    row.acknowledgedAt = now;
    return userId.length > 0;
  }
}
