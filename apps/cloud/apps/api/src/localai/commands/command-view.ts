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

/**
 * The params as the UI, the history and the audit trail may see them. `music.generate` carries the person's lyrics and
 * signed object-storage URLs (a write capability until they expire): only the node gets them, at claim time. Here they
 * become sizes and flags. Every other type is returned as stored.
 */
export function redactParams(type: string, params: unknown): unknown {
  if (type !== 'localai.music.generate' || typeof params !== 'object' || params === null) return params;
  const p = params as Record<string, unknown>;
  const lyrics = typeof p.lyrics === 'string' ? p.lyrics : '';
  const results = Array.isArray(p.results) ? p.results : [];
  return {
    request_id: p.request_id,
    lyrics_bytes: new TextEncoder().encode(lyrics).length,
    duration_s: p.duration_s,
    language: p.language,
    variations: p.variations,
    has_sample: p.sample !== undefined && p.sample !== null,
    ...(p.sample_mode !== undefined ? { sample_mode: p.sample_mode } : {}),
    results: results.length,
  };
}

const iso = (date: Date | null): string | null => (date ? date.toISOString() : null);

/** `row` as the UI sees it; `labNodeId` is the node's `ai-nN` id. */
export function toCommandView(row: LocalaiCommand, labNodeId: string, options: { withResult?: boolean } = {}): CommandView {
  const view: CommandView = {
    id: row.id,
    nodeId: labNodeId,
    type: row.type,
    status: row.status,
    params: redactParams(row.type, row.params),
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
