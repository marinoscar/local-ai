import type { UsageResponse, UsageRow } from './usageTypes';

export const USAGE_NOW = new Date('2026-11-04T12:00:00.000Z');

const row = (key: string, requests: number, promptTokens: number, completionTokens: number, nodes?: UsageRow['nodes']): UsageRow => ({ key, requests, promptTokens, completionTokens, ...(nodes ? { nodes } : {}) });

/** Three days of a seven-day range: a busy day with three nodes, a quiet one and an empty one (rows arrive oldest first). */
export function usageByDay(): UsageResponse {
  return {
    scope: 'own',
    keyId: null,
    from: '2026-11-02',
    to: '2026-11-04',
    groupBy: 'day',
    totals: { requests: 420, promptTokens: 1_100_000, completionTokens: 130_000 },
    rows: [
      row('2026-11-02', 412, 1_000_000, 120_000, [
        { nodeId: 'ai-n1', requests: 251 },
        { nodeId: 'ai-n2', requests: 124 },
        { nodeId: 'ai-n3', requests: 37 },
      ]),
      row('2026-11-03', 8, 100_000, 10_000, [{ nodeId: 'ai-n1', requests: 8 }]),
      row('2026-11-04', 0, 0, 0, []),
    ],
  };
}

export function usageByModel(): UsageResponse {
  return {
    scope: 'own',
    keyId: null,
    from: '2026-10-29',
    to: '2026-11-04',
    groupBy: 'model',
    totals: { requests: 14, promptTokens: 5_000, completionTokens: 900 },
    rows: [
      row('qwen3-coder-next-q4km', 10, 4_000, 800),
      row('qwen3.5-9b-q4km', 3, 1_000, 100),
      { ...row('x', 1, 0, 0), key: null },
    ],
  };
}

export const emptyUsage = (): UsageResponse => ({
  scope: 'own',
  keyId: null,
  from: '2026-10-06',
  to: '2026-11-04',
  groupBy: 'day',
  totals: { requests: 0, promptTokens: 0, completionTokens: 0 },
  rows: [row('2026-11-04', 0, 0, 0)],
});
