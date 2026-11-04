// The query of `GET /api/localai/audit` and the shapes it answers (docs/specs/cloud-app.md section 3.9), Zod-validated.
import { z } from 'zod';

import { AUDIT_ACTION_PREFIX } from './audit-actions';

/** Fixed by the spec: the page size of the audit trail. */
export const AUDIT_PAGE_SIZE = 50;
/** `from` defaults to this long before now. */
export const AUDIT_DEFAULT_WINDOW_DAYS = 30;

const DAY_ONLY = /^\d{4}-\d{2}-\d{2}$/;

/** A browser sends an empty field as `""`: that means "not set". */
const blankIsUnset = (value: unknown): unknown => (typeof value === 'string' && value.trim() === '' ? undefined : value);

/**
 * An instant: a full ISO date-time (`2026-11-02T15:04:00Z`) or a calendar day (`2026-11-02`), which `from` reads as the
 * start of that UTC day and `to` as its last millisecond, so a day range is inclusive on both ends.
 */
function instant(edge: 'from' | 'to') {
  return z.preprocess(
    blankIsUnset,
    z
      .string()
      .max(40)
      .optional()
      .transform((value, ctx): Date | undefined => {
        if (value === undefined) return undefined;
        const date = DAY_ONLY.test(value) ? new Date(`${value}T${edge === 'from' ? '00:00:00.000' : '23:59:59.999'}Z`) : new Date(value);
        if (Number.isNaN(date.getTime()) || (!DAY_ONLY.test(value) && !/^\d{4}-\d{2}-\d{2}T/.test(value))) {
          ctx.addIssue({ code: 'custom', message: `${edge} must be a date (2026-11-02) or an ISO date-time` });
          return z.NEVER;
        }
        return date;
      }),
  );
}

export const AuditQuerySchema = z
  .object({
    /** The lab node id (`ai-n3`). */
    nodeId: z.preprocess(blankIsUnset, z.string().regex(/^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/).optional()),
    /** A prefix of the action: `localai.command.` or a whole action. Always inside the `localai.` namespace. */
    action: z.preprocess(blankIsUnset, z.string().max(80).startsWith(AUDIT_ACTION_PREFIX).regex(/^[a-z0-9_.]+$/).optional()),
    from: instant('from'),
    to: instant('to'),
    page: z.coerce.number().int().min(1).max(100_000).default(1),
  })
  .strict()
  .refine((query) => query.from === undefined || query.to === undefined || query.from.getTime() <= query.to.getTime(), {
    message: 'from must not be after to',
    path: ['from'],
  });
export type AuditQuery = z.infer<typeof AuditQuerySchema>;

export interface AuditRowDto {
  id: string;
  /** ISO instant. */
  at: string;
  actor: { kind: 'user' | 'node' | 'system'; label: string };
  action: string;
  targetType: string;
  targetId: string;
  summary: string;
  /** An in-app path to the target, or null. */
  link: string | null;
}

export interface AuditPageDto {
  items: AuditRowDto[];
  page: number;
  pageSize: number;
  total: number;
  totalPages: number;
}

/** What the filter bar offers: the known actions by group, and every node (retired ones too: their trail matters). */
export interface AuditFiltersDto {
  groups: Array<{ prefix: string; label: string }>;
  actions: Array<{ action: string; label: string }>;
  nodes: Array<{ nodeId: string; displayName: string; phase: string }>;
  pageSize: number;
  defaultWindowDays: number;
}
