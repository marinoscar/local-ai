// How the usage rows read (the wireframe of docs/specs/gateway.md section 3.8). Pure.
import { compactCount } from './format';
import type { UsageRow } from './usageTypes';

/** "412 req" / "1 req". */
export function describeRequests(count: number): string {
  return `${count.toLocaleString('en-US')} ${count === 1 ? 'request' : 'requests'}`;
}

/** "1.2M tokens": prompt plus completion. */
export function describeTokens(row: Pick<UsageRow, 'promptTokens' | 'completionTokens'>): string {
  const total = row.promptTokens + row.completionTokens;
  return `${compactCount(total)} ${total === 1 ? 'token' : 'tokens'}`;
}

/** "1M in · 200k out". */
export function describeTokenSplit(row: Pick<UsageRow, 'promptTokens' | 'completionTokens'>): string {
  return `${compactCount(row.promptTokens)} in · ${compactCount(row.completionTokens)} out`;
}

/** "ai-n1 61 % · ai-n2 30 % · ai-n3 9 %": each node's share of the day's requests that reached a node; "" for none. */
export function describeNodeShares(nodes: ReadonlyArray<{ nodeId: string; requests: number }> | undefined): string {
  if (!nodes || nodes.length === 0) return '';
  const total = nodes.reduce((sum, node) => sum + node.requests, 0);
  if (total === 0) return '';
  return nodes.map((node) => `${node.nodeId} ${Math.round((node.requests / total) * 100)} %`).join(' · ');
}

/** The label of a row: the day, the model or the node; "(none)" for requests that had no model or node. */
export function describeKey(key: string | null): string {
  return key === null ? '(none)' : key;
}

/** The bar's width, 0 to 100, against the busiest row; any request at all shows at least a sliver. */
export function barPercent(requests: number, max: number): number {
  if (requests <= 0 || max <= 0) return 0;
  return Math.max(2, Math.round((requests / max) * 100));
}
