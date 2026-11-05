// Step 3 of the selection (docs/specs/gateway.md section 4.3): a request for a model that no node has loaded, when the
// model's policy allows an on-demand load. The gateway queues an ordinary `localai.model.load` through the command plane,
// waits for the routing table to show the model loaded on that node, and then goes on at step 1. It never evicts a model
// that is serving requests.
//
//   none loaded + policy.onDemandLoad
//     -> loadable candidates (priority, preference, fewest busy loaded models, node id), minus the nodes already tried
//     -> eviction guard: loading the model unloads EVERY other loaded model when `loadedCount + 1 > models_max`; a node
//        where that would unload a model with `inflight > 0` is skipped
//     -> first eligible node: enqueue `model.load { model_id, wait: true, unload_others, set_default: false }` with
//        source `gateway` (dedup: concurrent requests share one command), then wait up to `policy.loadWaitS`
//     -> loaded: select again   |   timeout: 503 LAI-GW-022 (the load keeps going)   |   abort: the request ends, the load stays
//     -> every loadable node would evict a busy model: 503 LAI-GW-021
//
// `OnDemandCandidateSource` WRAPS the table's source (`GATEWAY_CANDIDATE_SOURCE`): when the table finds a loaded candidate
// (or every one saturated), or the policy has no on-demand load, the wrapped source answers exactly as before.
//
// A model that is already LOADING (a request arrived while an earlier one's load runs, or after its wait timed out) is
// in no candidate list (the table lists `loaded` and `unloaded` rows only), so the source looks the active
// `localai.model.load` command up (`ActiveLoadReader`) and joins its wait instead of answering 503 LAI-GW-021.
//
// No prompt, no body and no token passes through here. The audit row of the command carries the key's display prefix.
import { GatewayError } from '../errors';
import type { CandidateChoice, CandidateRequest, CandidateSource } from '../attempts';
import type { CommandSource } from '../../commands/command-enqueuer';
import type { LoadableCandidate, RoutingPolicy, SelectResult } from './routing.types';

/** A node where the load is allowed, and the `unload_others` the command carries. */
export interface OnDemandPlanEntry {
  candidate: LoadableCandidate;
  unloadOthers: boolean;
}

export interface OnDemandPlan {
  /** In the order of the candidates handed in. */
  eligible: OnDemandPlanEntry[];
  /** Nodes left out because the load would unload a model that is serving requests. */
  skippedBusy: string[];
}

/** How many models a node's router keeps loaded, when its snapshot did not say: the smallest router there is. */
export const ASSUMED_MODELS_MAX = 1;

/**
 * The eviction guard of the spec, for one node: loading one more model unloads the others exactly when
 * `loadedCount + 1 > models_max`, and then every other loaded model goes. A node is skippable only in that case, and only
 * if one of those models has `inflight > 0`. Pure.
 */
export function wouldEvict(candidate: Pick<LoadableCandidate, 'loadedOnNode' | 'modelsMax'>): { unloadOthers: boolean; busy: boolean } {
  const modelsMax = candidate.modelsMax ?? ASSUMED_MODELS_MAX;
  const unloadOthers = candidate.loadedOnNode.length + 1 > modelsMax;
  const busy = unloadOthers && candidate.loadedOnNode.some((model) => model.inflight > 0);
  return { unloadOthers, busy };
}

/**
 * Of the table's loadable candidates (already ordered by `priority`, preference, fewest busy loaded models and node id),
 * those a load may go to. A node already `tried` by this request is left out, so a node that just failed it is not asked
 * to load the model again. Pure: the whole table of cases is a unit test.
 */
export function planOnDemand(candidates: readonly LoadableCandidate[], tried: ReadonlySet<string>): OnDemandPlan {
  const eligible: OnDemandPlanEntry[] = [];
  const skippedBusy: string[] = [];
  for (const candidate of candidates) {
    if (tried.has(candidate.nodeId)) continue;
    const verdict = wouldEvict(candidate);
    if (verdict.busy) skippedBusy.push(candidate.nodeId);
    else eligible.push({ candidate, unloadOthers: verdict.unloadOthers });
  }
  return { eligible, skippedBusy };
}

/** The params of the command, always explicit (the Zod default of `unload_others` is `true`). */
export function loadParams(modelId: string, unloadOthers: boolean): { model_id: string; wait: true; unload_others: boolean; set_default: false } {
  return { model_id: modelId, wait: true, unload_others: unloadOthers, set_default: false };
}

// ---- ports ----------------------------------------------------------------------------------------------------------

/** The part of the routing table the on-demand path uses; `RoutingTable` satisfies it. */
export interface OnDemandRouting {
  select(modelId: string, policy: RoutingPolicy, tried: ReadonlySet<string>): SelectResult;
  loadableCandidates(modelId: string, policy: RoutingPolicy): LoadableCandidate[];
  waitForLoaded(modelId: string, nodeId: string, timeoutMs: number, signal?: AbortSignal): Promise<boolean>;
  routableNodes(): Array<{ nodeId: string; publicHost: string }>;
  /** Re-reads the database (coalesced): the poll fallback of the wait. */
  rebuild(): Promise<void>;
}

/** The command plane's queue port (`LOCALAI_COMMAND_ENQUEUER`). */
export interface LoadEnqueuer {
  enqueue(nodeId: string, type: string, rawParams: unknown, actor: { userId: string | null }, source: CommandSource): Promise<{ id: string; created: boolean }>;
}

/** A `localai.model.load` that is queued or running. */
export interface ActiveLoad {
  commandId: string;
  /** The lab id (`ai-n2`). */
  nodeId: string;
}

export type CommandOutcome = 'active' | 'succeeded' | 'failed';

/** Reads the command plane's rows for the on-demand path (reads only). */
export interface ActiveLoadReader {
  /** The active load of `modelId` on any node, or null. */
  find(modelId: string): Promise<ActiveLoad | null>;
  /** How a command stands: `failed` covers failed, expired and cancelled. Null when it is gone. */
  outcome(commandId: string): Promise<CommandOutcome | null>;
}

/** Injection token of the {@link ActiveLoadReader}. */
export const ACTIVE_LOAD_READER = Symbol('LOCALAI_GATEWAY_ACTIVE_LOAD_READER');

/** Records that the gateway queued a load (`localai.command.requested`, source `gateway`, the key prefix; no prompt). */
export interface LoadAudit {
  requested(input: { commandId: string; nodeId: string; params: Record<string, unknown>; keyPrefix: string | null }): Promise<void>;
}

export interface OnDemandDeps {
  /** The table's source: it answers whenever the on-demand path does not apply. */
  inner: CandidateSource;
  routing: OnDemandRouting;
  commands: LoadEnqueuer;
  loads: ActiveLoadReader;
  audit: LoadAudit;
  log: { warn(line: string): void };
  /** How often the wait looks at the table and the command (spec: a poll fallback every 1 s). */
  pollMs?: number;
  /** How many times a request may load, wait and select again before it gives up with LAI-GW-022. */
  maxRounds?: number;
}

export const ON_DEMAND_POLL_MS = 1000;
export const ON_DEMAND_MAX_ROUNDS = 3;

type Waited = 'loaded' | 'timeout' | 'aborted' | 'failed';

export class OnDemandCandidateSource implements CandidateSource {
  private readonly pollMs: number;
  private readonly maxRounds: number;

  constructor(private readonly deps: OnDemandDeps) {
    this.pollMs = deps.pollMs ?? ON_DEMAND_POLL_MS;
    this.maxRounds = deps.maxRounds ?? ON_DEMAND_MAX_ROUNDS;
  }

  async next(request: CandidateRequest): Promise<CandidateChoice> {
    const { inner, routing } = this.deps;
    const { model, tried, signal } = request;
    if (!model.policy.onDemandLoad) return inner.next(request);

    // The deadline covers the whole request: every round spends from the same `loadWaitS`.
    const deadline = Date.now() + model.policy.loadWaitS * 1000;
    for (let round = 1; round <= this.maxRounds; round += 1) {
      if (signal.aborted) return { kind: 'none', error: null };
      // Steps 1 and 2: a loaded node (or all of them saturated) is the table's answer, as without on-demand.
      if (routing.select(model.modelId, model.policy, tried).kind !== 'none_loaded') return inner.next(request);

      const plan = planOnDemand(routing.loadableCandidates(model.modelId, model.policy), tried);
      let waited: Waited;
      if (plan.eligible.length > 0) {
        const nodeId = await this.queueLoad(request, plan.eligible);
        if (nodeId === null) return { kind: 'none', error: new GatewayError('LAI-GW-052') };
        waited = await this.waitFor(model.modelId, nodeId.nodeId, nodeId.commandId, deadline, signal);
      } else {
        // Nothing to start: perhaps a load is already running (the model shows as "loading", in no candidate list).
        const active = await this.joinable(model.modelId, tried);
        if (active) {
          waited = await this.waitFor(model.modelId, active.nodeId, active.commandId, deadline, signal);
        } else if (plan.skippedBusy.length > 0) {
          // Every node that could load it would unload a model that is serving requests.
          return { kind: 'none', error: request.attempt === 1 ? new GatewayError('LAI-GW-021') : null };
        } else {
          return inner.next(request);
        }
      }

      if (waited === 'loaded') continue; // back to step 1
      if (waited === 'aborted') return { kind: 'none', error: null }; // the attempt loop sees the aborted signal
      if (waited === 'failed') return { kind: 'none', error: new GatewayError('LAI-GW-021') };
      return { kind: 'none', error: new GatewayError('LAI-GW-022') };
    }
    return { kind: 'none', error: new GatewayError('LAI-GW-022') };
  }

  /** Enqueues on the first node that accepts the command. Null when none did. */
  private async queueLoad(request: CandidateRequest, entries: readonly OnDemandPlanEntry[]): Promise<{ nodeId: string; commandId: string } | null> {
    const { model } = request;
    for (const { candidate, unloadOthers } of entries) {
      const params = loadParams(model.modelId, unloadOthers);
      try {
        const queued = await this.deps.commands.enqueue(candidate.nodeId, 'model.load', params, { userId: null }, 'gateway');
        if (queued.created) {
          // Audit runs after the write; a failure to audit never fails the request.
          try {
            await this.deps.audit.requested({ commandId: queued.id, nodeId: candidate.nodeId, params, keyPrefix: model.keyPrefix ?? null });
          } catch (error) {
            this.deps.log.warn(JSON.stringify({ event: 'gateway.on_demand_audit_failed', node_id: candidate.nodeId, error: errorName(error) }));
          }
        }
        return { nodeId: candidate.nodeId, commandId: queued.id };
      } catch (error) {
        this.deps.log.warn(JSON.stringify({ event: 'gateway.on_demand_enqueue_failed', model_id: model.modelId, node_id: candidate.nodeId, error: errorName(error) }));
      }
    }
    return null;
  }

  /** An active load of the model on a node the gateway can route to and this request has not tried. */
  private async joinable(modelId: string, tried: ReadonlySet<string>): Promise<ActiveLoad | null> {
    let active: ActiveLoad | null;
    try {
      active = await this.deps.loads.find(modelId);
    } catch {
      return null;
    }
    if (!active || tried.has(active.nodeId)) return null;
    return this.deps.routing.routableNodes().some((node) => node.nodeId === active.nodeId) ? active : null;
  }

  /**
   * Waits until the table shows the model loaded on the node. Looks again every `pollMs`: the table is rebuilt (the poll
   * fallback of the event-driven wait) and the command is checked, so a load that failed ends the wait at once.
   */
  private async waitFor(modelId: string, nodeId: string, commandId: string, deadline: number, signal: AbortSignal): Promise<Waited> {
    const { routing, loads } = this.deps;
    for (;;) {
      if (signal.aborted) return 'aborted';
      const remaining = deadline - Date.now();
      if (remaining <= 0) return 'timeout';
      const loaded = await routing.waitForLoaded(modelId, nodeId, Math.min(this.pollMs, remaining), signal);
      if (loaded) return 'loaded';
      if (signal.aborted) return 'aborted';
      await routing.rebuild();
      if ((await routing.waitForLoaded(modelId, nodeId, 0, signal)) === true) return 'loaded';
      try {
        if ((await loads.outcome(commandId)) === 'failed') return 'failed';
      } catch {
        // A failed read only loses the early exit; the deadline still ends the wait.
      }
    }
  }
}

function errorName(error: unknown): string {
  return error instanceof Error ? error.name : 'error';
}
