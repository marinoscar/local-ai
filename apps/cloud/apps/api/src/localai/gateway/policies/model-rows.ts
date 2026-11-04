// The rows of `GET /api/localai/gateway/models`: every catalog model with its policy and its live state, built from the
// SAME inputs as `/v1/models` (the directory and the routing table's healthy candidates), so the page and the listing
// cannot disagree. Pure.
import type { ModelDirectory } from '../models/resolve';
import { policyOf } from '../models/resolve';
import type { ModelPolicy } from '../models/policy';
import type { SelectedCandidate } from '../routing/routing.types';
import type { ModelLiveState, ModelPolicyDto, ModelRowDto } from './policies.schemas';

export type HealthyCandidates = (modelId: string) => readonly SelectedCandidate[];

/** The stored columns the page shows besides the policy itself. */
export interface PolicyMeta {
  updatedAt: Date;
}

const compare = (a: string, b: string): number => (a < b ? -1 : a > b ? 1 : 0);

export function toPolicyDto(policy: ModelPolicy, meta: PolicyMeta | undefined): ModelPolicyDto {
  return {
    enabled: policy.enabled,
    aliases: [...policy.aliases],
    onDemandLoad: policy.onDemandLoad,
    loadWaitS: policy.loadWaitS,
    firstByteTimeoutS: policy.firstByteTimeoutS,
    preferredNodes: [...policy.preferredNodes],
    stored: meta !== undefined,
    updatedAt: meta ? meta.updatedAt.toISOString() : null,
  };
}

/** One model's row. */
export function buildModelRow(
  directory: ModelDirectory,
  modelId: string,
  candidatesOf: HealthyCandidates,
  metaOf: (modelId: string) => PolicyMeta | undefined,
): ModelRowDto | null {
  const hints = directory.catalog?.models.get(modelId);
  if (!hints) return null;
  const policy = policyOf(directory, modelId);
  const candidates = candidatesOf(modelId);
  const loaded = candidates.filter((candidate) => candidate.status === 'loaded');
  const loadable = candidates.filter((candidate) => candidate.status === 'loadable');

  // The listing's rule (listing.ts): loaded wins; loadable counts only with on-demand load on; disabled models are not listed.
  let state: ModelLiveState;
  let listed = false;
  if (loaded.length > 0) {
    state = 'loaded';
    listed = policy.enabled;
  } else if (policy.onDemandLoad && loadable.length > 0) {
    state = 'loadable';
    listed = policy.enabled;
  } else {
    state = loadable.length > 0 ? 'unloaded' : 'none';
  }

  const nodes = [...loaded, ...loadable].map((candidate) => ({ nodeId: candidate.nodeId, status: candidate.status })).sort((a, b) => compare(a.nodeId, b.nodeId));
  return { modelId, displayName: hints.displayName, roles: [...hints.roles], policy: toPolicyDto(policy, metaOf(modelId)), state, listed, nodes };
}

/**
 * Every catalog model for an administrator; for anyone else only the models `/v1/models` lists (enabled and offered),
 * so a disabled or unreachable model does not appear. Sorted: listed first, then by id.
 */
export function buildModelRows(
  directory: ModelDirectory,
  candidatesOf: HealthyCandidates,
  metaOf: (modelId: string) => PolicyMeta | undefined,
  admin: boolean,
): ModelRowDto[] {
  const rows: ModelRowDto[] = [];
  for (const modelId of directory.catalog?.models.keys() ?? []) {
    const row = buildModelRow(directory, modelId, candidatesOf, metaOf);
    if (row && (admin || row.listed)) rows.push(row);
  }
  return rows.sort((a, b) => Number(b.listed) - Number(a.listed) || compare(a.modelId, b.modelId));
}
