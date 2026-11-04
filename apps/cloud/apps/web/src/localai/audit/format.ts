// Text helpers of the audit page. Pure.
import type { AuditFilterState, AuditRow } from './types';

/** "03 Nov 2026, 15:04:00": a 24 hour clock, the viewer's zone unless a test names one. */
export function formatWhen(iso: string, timeZone?: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return iso;
  return new Intl.DateTimeFormat('en-GB', {
    day: '2-digit',
    month: 'short',
    year: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
    ...(timeZone ? { timeZone } : {}),
  }).format(date);
}

const short = (id: string) => (id.length > 12 ? id.slice(0, 8) : id);

/** What the Target column calls the row's target: "node ai-n3", "command 6b3c1c9e", "catalog 2026.11.02-1". */
export function targetLabel(row: Pick<AuditRow, 'targetType' | 'targetId'>): string {
  switch (row.targetType) {
    case 'localai_node':
      return `node ${short(row.targetId)}`;
    case 'localai_command':
      return `command ${short(row.targetId)}`;
    case 'localai_catalog':
      return `catalog ${row.targetId}`;
    case 'localai_alert':
      return `alert ${short(row.targetId)}`;
    case 'localai_vps':
      return row.targetId === 'route53' ? 'Route53 key' : `VPS ${short(row.targetId)}`;
    case 'localai_gateway_key':
      return `gateway key ${short(row.targetId)}`;
    case 'localai_model_policy':
      return `model policy ${short(row.targetId)}`;
    default:
      return `${row.targetType.replace(/^localai_/, '').replace(/_/g, ' ')} ${short(row.targetId)}`.trim();
  }
}

/** The start of a calendar day in the viewer's zone, as the ISO instant the API takes for `from`. */
export function dayStartIso(day: string): string | undefined {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(day);
  if (!match) return undefined;
  return new Date(Number(match[1]), Number(match[2]) - 1, Number(match[3]), 0, 0, 0, 0).toISOString();
}

/** The last millisecond of a calendar day in the viewer's zone, as the ISO instant the API takes for `to`. */
export function dayEndIso(day: string): string | undefined {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(day);
  if (!match) return undefined;
  return new Date(Number(match[1]), Number(match[2]) - 1, Number(match[3]), 23, 59, 59, 999).toISOString();
}

/** The query string of `GET /api/localai/audit` for the filter bar; empty fields are left out. */
export function auditQueryString(filters: AuditFilterState): string {
  const params = new URLSearchParams();
  if (filters.nodeId) params.set('nodeId', filters.nodeId);
  if (filters.action) params.set('action', filters.action);
  const from = dayStartIso(filters.from);
  const to = dayEndIso(filters.to);
  if (from) params.set('from', from);
  if (to) params.set('to', to);
  if (filters.page > 1) params.set('page', String(filters.page));
  return params.toString();
}

/** True when the range is backwards (the API would answer 400, so the page says so first). */
export function rangeIsBackwards(filters: Pick<AuditFilterState, 'from' | 'to'>): boolean {
  return filters.from !== '' && filters.to !== '' && filters.from > filters.to;
}
