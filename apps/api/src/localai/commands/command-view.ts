// The wire shape of a command for the UI routes (docs/specs/cloud-app.md section 3.9). `type` is the full registry
// name (`localai.model.load`), `nodeId` the lab id (`ai-n2`), never the row uuid. The list leaves `result` out (a
// result can be large and the history does not need it); the single-command route carries it.
import type { LocalaiCommand } from '@prisma/client';

export interface CommandView {
  id: string;
  nodeId: string;
  type: string;
  status: LocalaiCommand['status'];
  params: unknown;
  source: string;
  requestedById: string | null;
  attempts: number;
  expiresAt: string;
  createdAt: string;
  claimedAt: string | null;
  finishedAt: string | null;
  error: unknown;
  /** Present on the single-command route only. */
  result?: unknown;
}

const iso = (date: Date | null): string | null => (date ? date.toISOString() : null);

/** `row` as the UI sees it; `labNodeId` is the node's `ai-nN` id. */
export function toCommandView(row: LocalaiCommand, labNodeId: string, options: { withResult?: boolean } = {}): CommandView {
  const view: CommandView = {
    id: row.id,
    nodeId: labNodeId,
    type: row.type,
    status: row.status,
    params: row.params,
    source: row.source,
    requestedById: row.requestedById,
    attempts: row.attempts,
    expiresAt: row.expiresAt.toISOString(),
    createdAt: row.createdAt.toISOString(),
    claimedAt: iso(row.claimedAt),
    finishedAt: iso(row.finishedAt),
    error: row.error ?? null,
  };
  if (options.withResult) view.result = row.result ?? null;
  return view;
}
