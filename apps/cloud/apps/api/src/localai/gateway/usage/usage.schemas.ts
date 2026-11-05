// The query and the response of `GET /api/localai/gateway/usage` (docs/specs/gateway.md section 3.8), in Zod and types.
import { z } from 'zod';

const flag = z
  .enum(['1', '0', 'true', 'false'])
  .optional()
  .transform((value) => value === '1' || value === 'true');

const day = z.string().regex(/^\d{4}-\d{2}-\d{2}$/, 'must be a UTC date, YYYY-MM-DD').refine((value) => !Number.isNaN(Date.parse(`${value}T00:00:00Z`)) && new Date(`${value}T00:00:00Z`).toISOString().startsWith(value), 'must be a real date');

export const GROUP_BY = ['day', 'model', 'node'] as const;
export type UsageGroupBy = (typeof GROUP_BY)[number];

export const UsageQuerySchema = z
  .object({
    keyId: z.string().uuid().optional(),
    /** First UTC day, inclusive. Default: 29 days before `to`. */
    from: day.optional(),
    /** Last UTC day, inclusive. Default: today (UTC). */
    to: day.optional(),
    groupBy: z.enum(GROUP_BY).optional(),
    /** gateway:admin: every key, not only your own. */
    all: flag,
  })
  .strict();
export type UsageQuery = z.infer<typeof UsageQuerySchema>;

/** The longest range the API answers (days). */
export const MAX_RANGE_DAYS = 366;
/** The longest range `groupBy=model|node` (and the per-day node shares) can cover: they read the per-request rows. */
export const MAX_REQUEST_ROWS_DAYS = 7;
/** The range of the page when nothing is asked (days). */
export const DEFAULT_RANGE_DAYS = 30;

export interface UsageTotals {
  requests: number;
  promptTokens: number;
  completionTokens: number;
}

export interface UsageRowDto extends UsageTotals {
  /** The day (`YYYY-MM-DD`), the model id or the node id; `null` for requests with no model / no node (listing calls, early errors). */
  key: string | null;
  /** `groupBy=day` over a range of at most 7 days: the requests per node that day. */
  nodes?: Array<{ nodeId: string; requests: number }>;
}

export interface UsageDto {
  scope: 'own' | 'all';
  keyId: string | null;
  from: string;
  to: string;
  groupBy: UsageGroupBy;
  totals: UsageTotals;
  rows: UsageRowDto[];
}
