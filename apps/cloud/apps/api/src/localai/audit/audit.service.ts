// The read side of the audit trail (docs/specs/cloud-app.md sections 3.5 and 3.9): `audit_events` rows whose action
// starts with `localai.`, newest first, 50 to a page. Read-only; the writers are the owning items. The table's
// `created_at` and `(target_type, target_id)` indexes serve the time window and the node filter.
import { Injectable } from '@nestjs/common';
import type { Prisma } from '@prisma/client';

import { PrismaService } from '../../prisma/prisma.service';
import { scrubText } from '../commands/command-scrub';
import { AUDIT_ACTION_PREFIX, AUDIT_GROUPS, AUDIT_TARGET_NODE, KNOWN_AUDIT_ACTIONS } from './audit-actions';
import { auditActor, describeAudit, type NodeLabIds } from './audit-summary';
import { AUDIT_DEFAULT_WINDOW_DAYS, AUDIT_PAGE_SIZE, type AuditFiltersDto, type AuditPageDto, type AuditQuery, type AuditRowDto } from './audit.schemas';

const DAY_MS = 24 * 60 * 60 * 1000;

/**
 * The prefix as a LIKE pattern's literal text. Prisma's `startsWith` passes `%`, `_` and `\` through to `LIKE`
 * unescaped, so an unescaped `localai.node.mode_changed` would also match `localai.node.modexchanged`.
 */
export function likeLiteral(text: string): string {
  return text.replace(/[\\%_]/g, '\\$&');
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

@Injectable()
export class AuditService {
  constructor(private readonly prisma: PrismaService) {}

  async list(query: AuditQuery, now: Date = new Date()): Promise<AuditPageDto> {
    const from = query.from ?? new Date(now.getTime() - AUDIT_DEFAULT_WINDOW_DAYS * DAY_MS);
    const where: Prisma.AuditEventWhereInput = {
      action: { startsWith: likeLiteral(query.action ?? AUDIT_ACTION_PREFIX) },
      createdAt: { gte: from, ...(query.to ? { lte: query.to } : {}) },
      ...(query.nodeId ? await this.nodeClause(query.nodeId) : {}),
    };
    const [rows, total] = await Promise.all([
      this.prisma.auditEvent.findMany({
        where,
        orderBy: [{ createdAt: 'desc' }, { id: 'desc' }],
        skip: (query.page - 1) * AUDIT_PAGE_SIZE,
        take: AUDIT_PAGE_SIZE,
      }),
      this.prisma.auditEvent.count({ where }),
    ]);

    const emails = await this.emailsOf(rows.map((row) => row.actorUserId));
    const labIds = await this.labIdsOf(rows);
    const items = rows.map((row): AuditRowDto => {
      const described = describeAudit({ action: row.action, targetType: row.targetType, targetId: row.targetId, meta: row.meta }, labIds);
      return {
        id: row.id,
        at: row.createdAt.toISOString(),
        actor: auditActor(row.action, described.nodeId, row.actorUserId === null ? null : (emails.get(row.actorUserId) ?? null)),
        action: row.action,
        // Defence in depth: an id is an id, but a caller that put a token in one never gets it echoed.
        targetType: scrubText(row.targetType),
        targetId: scrubText(row.targetId),
        summary: described.summary,
        link: described.link,
      };
    });
    return { items, page: query.page, pageSize: AUDIT_PAGE_SIZE, total, totalPages: Math.max(1, Math.ceil(total / AUDIT_PAGE_SIZE)) };
  }

  /** The filter bar's options: the known actions (one constant) and every node, retired ones included. */
  async filters(): Promise<AuditFiltersDto> {
    const nodes = await this.prisma.localaiNode.findMany({ select: { nodeId: true, displayName: true, phase: true } });
    nodes.sort((a, b) => a.nodeId.localeCompare(b.nodeId, 'en', { numeric: true }));
    return {
      groups: AUDIT_GROUPS.map((group) => ({ ...group })),
      actions: KNOWN_AUDIT_ACTIONS.map((def) => ({ ...def })),
      nodes,
      pageSize: AUDIT_PAGE_SIZE,
      defaultWindowDays: AUDIT_DEFAULT_WINDOW_DAYS,
    };
  }

  /**
   * Rows about one node: its own target rows, plus the command and alert rows that name it in `meta.nodeId` (and the
   * binding refusals that name it in `meta.claimedNodeId`). The code writes the lab id (`ai-n3`); the spec's wording
   * allows the row uuid, so both are matched.
   */
  private async nodeClause(labNodeId: string): Promise<Prisma.AuditEventWhereInput> {
    const node = await this.prisma.localaiNode.findUnique({ where: { nodeId: labNodeId }, select: { id: true } });
    const ids = node ? [labNodeId, node.id] : [labNodeId];
    return {
      OR: [
        { targetType: AUDIT_TARGET_NODE, targetId: { in: ids } },
        ...ids.flatMap((id) => [{ meta: { path: ['nodeId'], equals: id } }, { meta: { path: ['claimedNodeId'], equals: id } }]),
      ],
    };
  }

  private async emailsOf(userIds: Array<string | null>): Promise<Map<string, string>> {
    const ids = [...new Set(userIds.filter((id): id is string => id !== null))];
    if (ids.length === 0) return new Map();
    const users = await this.prisma.user.findMany({ where: { id: { in: ids } }, select: { id: true, email: true } });
    return new Map(users.map((user) => [user.id, user.email]));
  }

  /** Lab ids for the node uuids the page's rows mention (usually none: the code writes lab ids). */
  private async labIdsOf(rows: Array<{ targetType: string; targetId: string; meta: unknown }>): Promise<NodeLabIds> {
    const wanted = new Set<string>();
    for (const row of rows) {
      if (row.targetType === AUDIT_TARGET_NODE && UUID.test(row.targetId)) wanted.add(row.targetId.toLowerCase());
      const meta = row.meta;
      if (meta !== null && typeof meta === 'object' && !Array.isArray(meta)) {
        for (const key of ['nodeId', 'claimedNodeId']) {
          const value = (meta as Record<string, unknown>)[key];
          if (typeof value === 'string' && UUID.test(value)) wanted.add(value.toLowerCase());
        }
      }
    }
    if (wanted.size === 0) return new Map();
    const nodes = await this.prisma.localaiNode.findMany({ where: { id: { in: [...wanted] } }, select: { id: true, nodeId: true } });
    return new Map(nodes.map((node) => [node.id, node.nodeId]));
  }
}
