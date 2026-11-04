// The PostgreSQL implementation of the alert store (`alert-store.ts`). No raw SQL: the "one active alert per
// (node, kind)" guarantee is the partial unique index `localai_alerts_one_active` (a raw-SQL index of the add_localai
// migration, intentional schema drift), and `createManyAndReturn({ skipDuplicates })` is `INSERT ... ON CONFLICT DO
// NOTHING RETURNING *`: it reports exactly the rows this call inserted, so under concurrency one evaluation raises and the
// others see nothing to announce. Every other write is an `updateMany` with the state it expects in its `where`.
import { Injectable } from '@nestjs/common';
import { deriveNodeHealth } from '@marinoscar/platform-api/nodes';
import { Prisma, type LocalaiAlert, type LocalaiNode } from '@prisma/client';

import { PrismaService } from '../../prisma/prisma.service';
import type { LabMode, LabPhase } from '../health/derive-health';
import { isAlertSeverity, type AlertSeverity } from './alert-kinds';
import { AlertSettingsReader } from './alert-settings';
import type { AlertListFilter, AlertRecord, AlertStore, EvaluationNode, NewAlert } from './alert-store';

type AlertRow = LocalaiAlert & { node: Pick<LocalaiNode, 'nodeId' | 'displayName'> | null };

const WITH_NODE = { node: { select: { nodeId: true, displayName: true } } } as const;

const asMode = (value: string | null): LabMode | null => (value === 'online' || value === 'offline' || value === 'survival' ? value : null);
const asDetails = (value: Prisma.JsonValue | null): Record<string, unknown> | null =>
  typeof value === 'object' && value !== null && !Array.isArray(value) ? (value as Record<string, unknown>) : null;

export function toRecord(row: AlertRow): AlertRecord {
  return {
    id: row.id,
    nodeRowId: row.nodeId,
    nodeId: row.node?.nodeId ?? null,
    nodeName: row.node?.displayName ?? null,
    kind: row.kind,
    code: row.code,
    // A severity outside the three words cannot be written by this code; read defensively all the same.
    severity: isAlertSeverity(row.severity) ? row.severity : 'warning',
    state: row.state,
    title: row.title,
    details: asDetails(row.details),
    firstRaisedAt: row.firstRaisedAt,
    lastSeenAt: row.lastSeenAt,
    clearedAt: row.clearedAt,
    acknowledgedAt: row.acknowledgedAt,
    notifiedAt: row.notifiedAt,
  };
}

const json = (value: Record<string, unknown>) => value as Prisma.InputJsonValue;

@Injectable()
export class PrismaAlertStore implements AlertStore {
  constructor(
    private readonly prisma: PrismaService,
    private readonly settings: AlertSettingsReader,
  ) {}

  async loadNodes(labNodeId?: string): Promise<EvaluationNode[]> {
    const rows = await this.prisma.localaiNode.findMany({
      where: { phase: { not: 'retired' }, ...(labNodeId !== undefined ? { nodeId: labNodeId } : {}) },
      orderBy: { nodeId: 'asc' },
    });
    if (rows.length === 0) return [];
    const workerIds = rows.map((row) => row.workerNodeId).filter((id): id is string => id !== null);
    const workers = workerIds.length
      ? await this.prisma.workerNode.findMany({ where: { id: { in: workerIds } }, select: { id: true, status: true, lastHeartbeatAt: true, registeredAt: true } })
      : [];
    const byId = new Map(workers.map((worker) => [worker.id, worker]));
    const { staleHeartbeatSeconds } = await this.settings.read();
    const now = new Date();
    return rows.map((row): EvaluationNode => {
      const worker = row.workerNodeId ? byId.get(row.workerNodeId) : undefined;
      const beat = worker?.lastHeartbeatAt ?? null;
      const contact = [beat, row.lastStateAt].filter((at): at is Date => at !== null).sort((a, b) => b.getTime() - a.getTime())[0] ?? null;
      return {
        rowId: row.id,
        displayName: row.displayName,
        nodeId: row.nodeId,
        phase: row.phase as LabPhase,
        mode: asMode(row.mode),
        lastStateAt: row.lastStateAt,
        lastState: row.lastState,
        agentVersion: row.agentVersion,
        catalogVersion: row.catalogVersion,
        catalogPin: row.catalogPin,
        keyReportedAt: row.keyReportedAt,
        // A node with no linked worker row counts as offline (spec 4.5).
        eab: worker ? deriveNodeHealth(worker, staleHeartbeatSeconds, now) : 'offline',
        lastContactAt: contact,
        enrolledAt: worker?.registeredAt ?? null,
      };
    });
  }

  async approvedCatalog() {
    const row = await this.prisma.localaiCatalogVersion.findFirst({ where: { status: 'approved' }, select: { version: true, approvedAt: true } });
    return row ? { version: row.version, approvedAt: row.approvedAt } : null;
  }

  async listActive(scope: { nodeRowId: string } | { all: true }): Promise<AlertRecord[]> {
    const rows = await this.prisma.localaiAlert.findMany({
      where: { state: 'active', ...('nodeRowId' in scope ? { nodeId: scope.nodeRowId } : {}) },
      include: WITH_NODE,
      orderBy: { firstRaisedAt: 'asc' },
    });
    return rows.map(toRecord);
  }

  async listActiveOfRetiredNodes(): Promise<AlertRecord[]> {
    const rows = await this.prisma.localaiAlert.findMany({ where: { state: 'active', node: { is: { phase: 'retired' } } }, include: WITH_NODE });
    return rows.map(toRecord);
  }

  async insert(alert: NewAlert, now: Date): Promise<AlertRecord | null> {
    const inserted = await this.prisma.localaiAlert.createManyAndReturn({
      data: [
        {
          nodeId: alert.nodeRowId,
          kind: alert.kind,
          code: alert.code,
          severity: alert.severity,
          state: 'active',
          title: alert.title,
          details: json(alert.details),
          firstRaisedAt: now,
          lastSeenAt: now,
        },
      ],
      skipDuplicates: true,
    });
    const row = inserted[0];
    if (!row) return null;
    const full = await this.prisma.localaiAlert.findUnique({ where: { id: row.id }, include: WITH_NODE });
    return full ? toRecord(full) : null;
  }

  async refresh(id: string, patch: { title: string; details: Record<string, unknown> }, now: Date): Promise<boolean> {
    const result = await this.prisma.localaiAlert.updateMany({
      where: { id, state: 'active' },
      data: { lastSeenAt: now, title: patch.title, details: json(patch.details) },
    });
    return result.count === 1;
  }

  async changeSeverity(
    id: string,
    change: { from: AlertSeverity; to: AlertSeverity; title: string; details: Record<string, unknown>; resetNotice: boolean },
    now: Date,
  ): Promise<boolean> {
    const result = await this.prisma.localaiAlert.updateMany({
      where: { id, state: 'active', severity: change.from },
      data: {
        severity: change.to,
        title: change.title,
        details: json(change.details),
        lastSeenAt: now,
        ...(change.resetNotice ? { notifiedAt: null, acknowledgedAt: null, acknowledgedById: null } : {}),
      },
    });
    return result.count === 1;
  }

  async clear(id: string, now: Date): Promise<boolean> {
    const result = await this.prisma.localaiAlert.updateMany({ where: { id, state: 'active' }, data: { state: 'cleared', clearedAt: now } });
    return result.count === 1;
  }

  async listDueForNotice(repeatBefore: Date): Promise<AlertRecord[]> {
    const rows = await this.prisma.localaiAlert.findMany({
      where: { state: 'active', OR: [{ notifiedAt: null }, { acknowledgedAt: null, notifiedAt: { lt: repeatBefore } }] },
      include: WITH_NODE,
      orderBy: { firstRaisedAt: 'asc' },
      take: 200,
    });
    return rows.map(toRecord);
  }

  async claimNotice(id: string, now: Date, repeatBefore: Date): Promise<boolean> {
    const result = await this.prisma.localaiAlert.updateMany({
      where: { id, state: 'active', OR: [{ notifiedAt: null }, { acknowledgedAt: null, notifiedAt: { lt: repeatBefore } }] },
      data: { notifiedAt: now },
    });
    return result.count === 1;
  }

  async get(id: string): Promise<AlertRecord | null> {
    const row = await this.prisma.localaiAlert.findUnique({ where: { id }, include: WITH_NODE });
    return row ? toRecord(row) : null;
  }

  async list(filter: AlertListFilter): Promise<AlertRecord[]> {
    const rows = await this.prisma.localaiAlert.findMany({
      where: {
        ...(filter.state === 'all' ? {} : { state: filter.state }),
        ...(filter.nodeId !== undefined ? { node: { is: { nodeId: filter.nodeId } } } : {}),
      },
      include: WITH_NODE,
      orderBy: filter.state === 'cleared' ? { clearedAt: 'desc' } : { firstRaisedAt: 'desc' },
      take: filter.limit,
    });
    return rows.map(toRecord);
  }

  async acknowledge(id: string, userId: string, now: Date): Promise<boolean> {
    const result = await this.prisma.localaiAlert.updateMany({
      where: { id, state: 'active', acknowledgedAt: null },
      data: { acknowledgedAt: now, acknowledgedById: userId },
    });
    return result.count === 1;
  }
}
