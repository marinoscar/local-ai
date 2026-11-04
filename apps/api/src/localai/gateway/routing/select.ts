// Candidate selection, steps 1 and 2 of docs/specs/gateway.md section 4.3, and the error choice of step 4. PURE: the
// table hands in candidates with their live counters already copied (`SelectedCandidate`); nothing here reads a clock
// or mutates anything, so the whole table of cases is tested without a database.
import {
  GW_NO_CAPACITY,
  GW_UNKNOWN_MODEL,
  type RoutingPolicy,
  type SelectedCandidate,
  type SelectResult,
  type UnavailableOutcome,
} from './routing.types';

/** Suspect by an attempt failure (until `suspectUntil`) or by the probe (until its next success). */
export interface LiveFlags {
  suspect: boolean;
  keyInvalid: boolean;
}

export interface RankInput {
  candidate: SelectedCandidate;
  flags: LiveFlags;
}

/** Saturation: `inflight >= queueFactor x slots`. */
export function isSaturated(candidate: Pick<SelectedCandidate, 'inflight' | 'slots'>, queueFactor: number): boolean {
  return candidate.inflight >= queueFactor * Math.max(1, candidate.slots);
}

/** Position in `preferredNodes`; a node that is not listed sorts after every listed one. */
export function preferenceRank(nodeId: string, policy: RoutingPolicy): number {
  const index = policy.preferredNodes.indexOf(nodeId);
  return index === -1 ? Number.MAX_SAFE_INTEGER : index;
}

/**
 * The order of the spec: `priority` asc, then position in `policy.preferredNodes`, then `inflight / slots` asc, then
 * `ewmaTtfbMs` asc, then `nodeId`. A node with no measured first byte sorts BEFORE a measured one at the same load
 * (as built): otherwise a node that has never answered would never be tried, and so never measured.
 */
export function compareCandidates(a: SelectedCandidate, b: SelectedCandidate, policy: RoutingPolicy): number {
  if (a.priority !== b.priority) return a.priority - b.priority;
  const preference = preferenceRank(a.nodeId, policy) - preferenceRank(b.nodeId, policy);
  if (preference !== 0) return preference;
  const load = a.inflight / Math.max(1, a.slots) - b.inflight / Math.max(1, b.slots);
  if (load !== 0) return load;
  const ewma = (a.ewmaTtfbMs ?? -1) - (b.ewmaTtfbMs ?? -1);
  if (ewma !== 0) return ewma;
  return a.nodeId < b.nodeId ? -1 : a.nodeId > b.nodeId ? 1 : 0;
}

/**
 * Steps 1 and 2. `tried` are the nodes the attempt loop already used for this request. Of the loaded candidates, those
 * that are suspect, `keyInvalid` or tried are not eligible; if none is eligible the answer is `none_loaded`; if some
 * are but all are saturated the answer is `saturated` (503 LAI-GW-023); otherwise the unsaturated ones, in order.
 */
export function selectLoaded(
  inputs: readonly RankInput[],
  policy: RoutingPolicy,
  tried: ReadonlySet<string>,
  queueFactor: number,
): SelectResult {
  const eligible = inputs.filter(
    ({ candidate, flags }) => candidate.status === 'loaded' && !flags.suspect && !flags.keyInvalid && !tried.has(candidate.nodeId),
  );
  if (eligible.length === 0) return { kind: 'none_loaded' };
  const open = eligible.filter(({ candidate }) => !isSaturated(candidate, queueFactor));
  if (open.length === 0) return { kind: 'saturated' };
  return { kind: 'candidates', candidates: open.map((input) => input.candidate).sort((a, b) => compareCandidates(a, b, policy)) };
}

/**
 * Step 4: nothing loaded and no on-demand load will happen. A model of the approved catalog that some node has present
 * is a capacity problem (503 LAI-GW-021: try later); anything else is an unknown model (404 LAI-GW-020).
 */
export function unavailableOutcome(input: { inApprovedCatalog: boolean; presentOnSomeNode: boolean }): UnavailableOutcome {
  return input.inApprovedCatalog && input.presentOnSomeNode
    ? { status: 503, code: GW_NO_CAPACITY }
    : { status: 404, code: GW_UNKNOWN_MODEL };
}
