// The wire shape of `GET /api/localai/gateway/usage` (docs/specs/gateway.md section 3.8), as
// `apps/api/src/localai/gateway/usage/usage.schemas.ts` returns it. Counts only: there is no prompt, no completion and no
// address anywhere in it.

export type UsageGroupBy = 'day' | 'model' | 'node';

export interface UsageTotals {
  requests: number;
  promptTokens: number;
  completionTokens: number;
}

export interface UsageRow extends UsageTotals {
  /** The UTC day (`YYYY-MM-DD`), the model id or the node id; `null` for requests with no model or no node. */
  key: string | null;
  /** `groupBy=day` over at most 7 days: requests per node that day. */
  nodes?: Array<{ nodeId: string; requests: number }>;
}

export interface UsageResponse {
  scope: 'own' | 'all';
  keyId: string | null;
  from: string;
  to: string;
  groupBy: UsageGroupBy;
  totals: UsageTotals;
  rows: UsageRow[];
}

/** What the page asks for. */
export interface UsageFilters {
  /** 7 or 30 UTC days ending today. */
  days: 7 | 30;
  groupBy: UsageGroupBy;
  /** One of your keys, or null for all. */
  keyId: string | null;
  /** gateway:admin: every user's keys. */
  all: boolean;
}
