// The gateway's in-memory routing table (docs/specs/gateway.md section 4.3).
//
// WHAT IT IS. A view of `localai_nodes` joined with `localai_node_models`, restricted to the nodes the cloud calls
// `gatewayRoutable` (cloud-app.md section 4.5), rebuilt on start, on `localai.node.state`, on `localai.node.key_reported`
// and every 10 s as a safety net. Rebuilds are coalesced: one runs at a time and any number of triggers during it
// schedule exactly one more.
//
// WHAT SURVIVES A REBUILD. The live counters are kept in maps keyed by node id (suspect, keyInvalid) and by node id
// plus model id (inflight, ewma first byte, unloaded marks), never inside the rebuilt candidates, so a rebuild cannot
// reset them. A mark "unloaded" lasts until the node's next snapshot (`localai.node.state`); `keyInvalid` lasts until the
// node reports a key again (`localai.node.key_reported`); a probe suspect until the probe's next success.
//
// WHAT IT NEVER DOES. It never blocks the request path on I/O (`select` and the hooks are synchronous reads of memory),
// never logs or exports `tailnetIp`, and never writes to the database.
import { Inject, Injectable, Logger, type OnApplicationBootstrap, type OnModuleDestroy, type OnModuleInit } from '@nestjs/common';
import { EVENT_BUS, type EventBus } from '@marinoscar/platform-api/host';

import { LOCALAI_CLOCK, type LocalaiClock } from '../../commands/command-events';
import { LOCALAI_NODE_KEY_REPORTED_EVENT, type NodeKeyReportedEvent } from '../../keys/node-key.events';
import { LOCALAI_NODE_STATE_CHANNEL, type LocalaiNodeStateEvent } from '../../node-link/state.service';
import { buildSnapshot, type RoutingSnapshot } from './build-candidates';
import { ROUTING_SOURCE, type RoutingSource } from './routing-source';
import { selectLoaded, unavailableOutcome, preferenceRank, type LiveFlags } from './select';
import {
  DEFAULT_QUEUE_FACTOR,
  EWMA_ALPHA,
  NO_POLICY,
  REBUILD_INTERVAL_MS,
  SUSPECT_MS,
  type Candidate,
  type CandidateRef,
  type LoadableCandidate,
  type RoutableNode,
  type RoutingPolicy,
  type SelectedCandidate,
  type SelectResult,
  type UnavailableOutcome,
} from './routing.types';

export interface RoutingOptions {
  /** Run the 10 s safety rebuild. Off in tests; the events and the start-up rebuild always run. */
  timers: boolean;
  rebuildIntervalMs: number;
}

/** Injection token of the {@link RoutingOptions}. */
export const ROUTING_OPTIONS = Symbol('LOCALAI_GATEWAY_ROUTING_OPTIONS');

/** The defaults: timers on, except under Jest and when the gateway is switched off (`LOCALAI_GATEWAY_ENABLED=false`). */
export function routingOptionsFromEnv(env: NodeJS.ProcessEnv = process.env): RoutingOptions {
  return { timers: env.NODE_ENV !== 'test' && env.LOCALAI_GATEWAY_ENABLED !== 'false', rebuildIntervalMs: REBUILD_INTERVAL_MS };
}

interface NodeLive {
  /** Epoch ms until which a failed attempt keeps the node out (0 = none). */
  suspectUntil: number;
  /** Set by the probe after three failures, cleared by its next success. */
  probeSuspect: boolean;
  /** The node answered 401 to the stored key; until the next `key_reported`. */
  keyInvalid: boolean;
}

interface PairLive {
  inflight: number;
  ewmaTtfbMs: number | null;
}

interface Waiter {
  modelId: string;
  nodeId: string;
  done(loaded: boolean): void;
}

const pairKey = (nodeId: string, modelId: string): string => `${nodeId}\u0000${modelId}`;

@Injectable()
export class RoutingTable implements OnModuleInit, OnApplicationBootstrap, OnModuleDestroy {
  private readonly logger = new Logger(RoutingTable.name);

  private snapshot: RoutingSnapshot = { candidates: new Map(), routable: new Map(), present: new Set() };
  private queueFactor = DEFAULT_QUEUE_FACTOR;

  private readonly nodes = new Map<string, NodeLive>();
  private readonly pairs = new Map<string, PairLive>();
  /** node id -> model ids marked unloaded until that node's next snapshot. */
  private readonly unloadedMarks = new Map<string, Set<string>>();
  private readonly waiters = new Set<Waiter>();

  private running: Promise<void> | null = null;
  private rerun = false;
  private stopped = false;
  private interval: ReturnType<typeof setInterval> | null = null;
  private unsubscribers: Array<() => void> = [];

  constructor(
    @Inject(ROUTING_SOURCE) private readonly source: RoutingSource,
    @Inject(EVENT_BUS) private readonly bus: EventBus,
    @Inject(LOCALAI_CLOCK) private readonly clock: LocalaiClock,
    @Inject(ROUTING_OPTIONS) private readonly options: RoutingOptions,
  ) {}

  onModuleInit(): void {
    this.unsubscribers = [
      this.bus.subscribe<LocalaiNodeStateEvent>(LOCALAI_NODE_STATE_CHANNEL, (event) => this.onNodeState(event)),
      this.bus.subscribe<NodeKeyReportedEvent>(LOCALAI_NODE_KEY_REPORTED_EVENT, (event) => this.onKeyReported(event)),
    ];
  }

  async onApplicationBootstrap(): Promise<void> {
    await this.rebuild();
    if (this.options.timers && !this.stopped) {
      this.interval = setInterval(() => void this.rebuild(), this.options.rebuildIntervalMs);
      this.interval.unref?.();
    }
  }

  onModuleDestroy(): void {
    this.stopped = true;
    if (this.interval) clearInterval(this.interval);
    this.interval = null;
    for (const unsubscribe of this.unsubscribers) unsubscribe();
    this.unsubscribers = [];
    for (const waiter of [...this.waiters]) waiter.done(false);
  }

  // ---- rebuild ------------------------------------------------------------------------------------------------

  /**
   * Rebuilds the table from the database. Coalesced: while one runs, any number of calls schedule exactly one more, and
   * every caller's promise settles when that last one has finished. Never rejects: a failed read keeps the old table.
   */
  rebuild(): Promise<void> {
    if (this.stopped) return Promise.resolve();
    if (this.running) {
      this.rerun = true;
      return this.running;
    }
    this.running = this.rebuildLoop();
    return this.running;
  }

  private async rebuildLoop(): Promise<void> {
    try {
      do {
        this.rerun = false;
        await this.rebuildOnce();
      } while (this.rerun && !this.stopped);
    } finally {
      this.running = null;
    }
  }

  private async rebuildOnce(): Promise<void> {
    try {
      const now = this.clock.now();
      const { rows, settings } = await this.source.load(now);
      if (this.stopped) return;
      this.snapshot = buildSnapshot(rows, now);
      this.queueFactor = settings.queueFactor;
      this.prune(new Set(rows.map((row) => row.nodeId)));
      this.logger.debug(`routing table rebuilt: nodes=${this.snapshot.routable.size} models=${this.snapshot.candidates.size}`);
      this.settleWaiters();
    } catch (error) {
      // Error NAME only: a driver message could carry connection details.
      this.logger.warn(`routing table rebuild failed, keeping the previous table: ${error instanceof Error ? error.name : 'unknown error'}`);
    }
  }

  /** Forgets live state of nodes that no longer exist, unless a request is still in flight on them. */
  private prune(known: ReadonlySet<string>): void {
    for (const [key, pair] of this.pairs) {
      const nodeId = key.slice(0, key.indexOf('\u0000'));
      if (!known.has(nodeId) && pair.inflight === 0) this.pairs.delete(key);
    }
    const busy = new Set<string>();
    for (const [key, pair] of this.pairs) if (pair.inflight > 0) busy.add(key.slice(0, key.indexOf('\u0000')));
    for (const nodeId of [...this.nodes.keys()]) if (!known.has(nodeId) && !busy.has(nodeId)) this.nodes.delete(nodeId);
    for (const nodeId of [...this.unloadedMarks.keys()]) if (!known.has(nodeId)) this.unloadedMarks.delete(nodeId);
  }

  // ---- events -------------------------------------------------------------------------------------------------

  private onNodeState(event: LocalaiNodeStateEvent): void {
    if (event && typeof event.nodeId === 'string') this.unloadedMarks.delete(event.nodeId);
    void this.rebuild();
  }

  private onKeyReported(event: NodeKeyReportedEvent): void {
    if (event && typeof event.nodeId === 'string') {
      const live = this.nodes.get(event.nodeId);
      if (live) live.keyInvalid = false;
    }
    void this.rebuild();
  }

  // ---- selection ----------------------------------------------------------------------------------------------

  /**
   * Steps 1 and 2 of the selection for `modelId`: the loaded, healthy, untried candidates in order, `saturated`, or
   * `none_loaded`. Synchronous: the request path never waits on I/O here.
   */
  select(modelId: string, policy: RoutingPolicy = NO_POLICY, tried: ReadonlySet<string> = new Set()): SelectResult {
    const now = this.clock.now().getTime();
    const inputs = this.viewOf(modelId, now).map((candidate) => ({ candidate, flags: this.flagsOf(candidate.nodeId, now) }));
    return selectLoaded(inputs, policy, tried, this.queueFactor);
  }

  /** Step 4: 503 LAI-GW-021 when `modelId` is in the approved catalog and some node has it present, else 404 LAI-GW-020. */
  unavailable(modelId: string, inApprovedCatalog: boolean): UnavailableOutcome {
    return unavailableOutcome({ inApprovedCatalog, presentOnSomeNode: this.snapshot.present.has(modelId) });
  }

  /**
   * The on-demand primitive of GW-09: candidates that are not loaded but may be (present, verified, loadable), on a
   * node that is healthy and whose key is valid, ordered by `priority`, preference, fewest loaded models with
   * `inflight > 0`, then node id. Each carries the node's loaded models with their inflight counts so the caller can
   * apply the "never evict a busy model" rule.
   */
  loadableCandidates(modelId: string, policy: RoutingPolicy = NO_POLICY): LoadableCandidate[] {
    const now = this.clock.now().getTime();
    const loadedByNode = new Map<string, Array<{ modelId: string; inflight: number }>>();
    for (const id of this.snapshot.candidates.keys()) {
      for (const candidate of this.viewOf(id, now)) {
        if (candidate.status !== 'loaded') continue;
        const list = loadedByNode.get(candidate.nodeId) ?? [];
        list.push({ modelId: candidate.modelId, inflight: candidate.inflight });
        loadedByNode.set(candidate.nodeId, list);
      }
    }
    const busy = (nodeId: string): number => (loadedByNode.get(nodeId) ?? []).filter((model) => model.inflight > 0).length;
    return this.viewOf(modelId, now)
      .filter((candidate) => candidate.status === 'loadable')
      .filter((candidate) => {
        const flags = this.flagsOf(candidate.nodeId, now);
        return !flags.suspect && !flags.keyInvalid;
      })
      .map((candidate): LoadableCandidate => ({ ...candidate, loadedOnNode: loadedByNode.get(candidate.nodeId) ?? [] }))
      .sort(
        (a, b) =>
          a.priority - b.priority ||
          preferenceRank(a.nodeId, policy) - preferenceRank(b.nodeId, policy) ||
          busy(a.nodeId) - busy(b.nodeId) ||
          (a.nodeId < b.nodeId ? -1 : a.nodeId > b.nodeId ? 1 : 0),
      );
  }

  /**
   * Resolves `true` as soon as the table shows `modelId` loaded on `nodeId`, `false` after `timeoutMs` (or when `signal`
   * aborts, or the table is shut down). The check runs after every rebuild; a `localai.node.state` event triggers one,
   * so the wait ends within a rebuild of the node's snapshot that reports the model loaded.
   */
  waitForLoaded(modelId: string, nodeId: string, timeoutMs: number, signal?: AbortSignal): Promise<boolean> {
    if (this.isLoadedOn(nodeId, modelId)) return Promise.resolve(true);
    if (signal?.aborted || this.stopped) return Promise.resolve(false);
    return new Promise<boolean>((resolve) => {
      let timer: ReturnType<typeof setTimeout> | null = null;
      const onAbort = (): void => waiter.done(false);
      const waiter: Waiter = {
        modelId,
        nodeId,
        done: (loaded) => {
          if (!this.waiters.delete(waiter)) return;
          if (timer) clearTimeout(timer);
          signal?.removeEventListener('abort', onAbort);
          resolve(loaded);
        },
      };
      this.waiters.add(waiter);
      timer = setTimeout(() => waiter.done(false), Math.max(0, timeoutMs));
      signal?.addEventListener('abort', onAbort, { once: true });
    });
  }

  private settleWaiters(): void {
    for (const waiter of [...this.waiters]) if (this.isLoadedOn(waiter.nodeId, waiter.modelId)) waiter.done(true);
  }

  private isLoadedOn(nodeId: string, modelId: string): boolean {
    const now = this.clock.now().getTime();
    return this.viewOf(modelId, now).some((candidate) => candidate.nodeId === nodeId && candidate.status === 'loaded');
  }

  // ---- hooks of the attempt loop ------------------------------------------------------------------------------

  /** A candidate was chosen: `inflight` counts from here until {@link release}. */
  acquire(ref: CandidateRef): void {
    this.pair(ref).inflight += 1;
  }

  /** The response ended (or the attempt failed). Never goes below zero. */
  release(ref: CandidateRef): void {
    const pair = this.pair(ref);
    pair.inflight = Math.max(0, pair.inflight - 1);
  }

  /** A failed attempt: the node is out for `ms` (30 s). A longer existing suspicion is kept. */
  markSuspect(ref: CandidateRef, ms: number = SUSPECT_MS): void {
    const live = this.node(ref.nodeId);
    live.suspectUntil = Math.max(live.suspectUntil, this.clock.now().getTime() + ms);
  }

  /** The node answered 401 to the stored key: skipped until the next `localai.node.key_reported` for it. */
  markKeyInvalid(ref: Pick<CandidateRef, 'nodeId'>): void {
    this.node(ref.nodeId).keyInvalid = true;
  }

  /** Upstream said the model is not loaded: treated as unloaded until the node's next snapshot. */
  markUnloaded(nodeId: string, modelId: string): void {
    const marks = this.unloadedMarks.get(nodeId) ?? new Set<string>();
    marks.add(modelId);
    this.unloadedMarks.set(nodeId, marks);
  }

  /** A first byte arrived after `ms`: updates the exponentially weighted average (alpha 0.2). */
  recordTtfb(ref: CandidateRef, ms: number): void {
    if (!Number.isFinite(ms) || ms < 0) return;
    const pair = this.pair(ref);
    pair.ewmaTtfbMs = pair.ewmaTtfbMs === null ? ms : EWMA_ALPHA * ms + (1 - EWMA_ALPHA) * pair.ewmaTtfbMs;
  }

  /** Used by the probe: `true` after three failed checks, `false` after the next success. */
  setProbeSuspect(nodeId: string, suspect: boolean): void {
    this.node(nodeId).probeSuspect = suspect;
  }

  // ---- reads --------------------------------------------------------------------------------------------------

  /** "Gateway: N nodes" of the Lab header: the nodes the table admits. */
  routableNodeCount(): number {
    return this.snapshot.routable.size;
  }

  /** The nodes the probe checks: id and public host (never an address that is not public). */
  routableNodes(): RoutableNode[] {
    return [...this.snapshot.routable.values()].map(({ nodeId, publicHost }) => ({ nodeId, publicHost }));
  }

  /** Nodes of the table the gateway marked `keyInvalid` (the `localai.gateway.keys` doctor check). */
  keyInvalidNodes(): string[] {
    return [...this.snapshot.routable.keys()].filter((nodeId) => this.nodes.get(nodeId)?.keyInvalid === true).sort();
  }

  /**
   * The candidates of `modelId` a request could use right now: not suspect and not `keyInvalid` (the filter of step 1 of
   * the selection), each with its live state (a model marked unloaded counts as `loadable`, or is left out when it cannot
   * be loaded). `/v1/models` (GW-05) lists exactly what this returns, so every listed id is requestable.
   */
  healthyCandidates(modelId: string): SelectedCandidate[] {
    const now = this.clock.now().getTime();
    return this.viewOf(modelId, now).filter((candidate) => {
      const flags = this.flagsOf(candidate.nodeId, now);
      return !flags.suspect && !flags.keyInvalid;
    });
  }

  isSuspect(nodeId: string): boolean {
    return this.flagsOf(nodeId, this.clock.now().getTime()).suspect;
  }

  /** Current inflight of a candidate (for tests and the usage log). */
  inflightOf(ref: CandidateRef): number {
    return this.pairs.get(pairKey(ref.nodeId, ref.modelId))?.inflight ?? 0;
  }

  // ---- internals ----------------------------------------------------------------------------------------------

  private node(nodeId: string): NodeLive {
    let live = this.nodes.get(nodeId);
    if (!live) {
      live = { suspectUntil: 0, probeSuspect: false, keyInvalid: false };
      this.nodes.set(nodeId, live);
    }
    return live;
  }

  private pair(ref: CandidateRef): PairLive {
    const key = pairKey(ref.nodeId, ref.modelId);
    let live = this.pairs.get(key);
    if (!live) {
      live = { inflight: 0, ewmaTtfbMs: null };
      this.pairs.set(key, live);
    }
    return live;
  }

  private flagsOf(nodeId: string, nowMs: number): LiveFlags {
    const live = this.nodes.get(nodeId);
    return { suspect: live ? live.probeSuspect || nowMs < live.suspectUntil : false, keyInvalid: live?.keyInvalid ?? false };
  }

  /**
   * The candidates of one model with their live counters. A candidate marked unloaded counts as `loadable` when it can
   * be loaded, and is left out when it cannot.
   */
  private viewOf(modelId: string, nowMs: number): SelectedCandidate[] {
    const list: Candidate[] = this.snapshot.candidates.get(modelId) ?? [];
    const result: SelectedCandidate[] = [];
    for (const candidate of list) {
      let status = candidate.status;
      if (status === 'loaded' && this.unloadedMarks.get(candidate.nodeId)?.has(modelId)) {
        if (!candidate.canLoad) continue;
        status = 'loadable';
      }
      const pair = this.pairs.get(pairKey(candidate.nodeId, modelId));
      const node = this.nodes.get(candidate.nodeId);
      result.push({
        ...candidate,
        status,
        inflight: pair?.inflight ?? 0,
        ewmaTtfbMs: pair?.ewmaTtfbMs ?? null,
        suspectUntil: node && node.suspectUntil > nowMs ? node.suspectUntil : 0,
      });
    }
    return result;
  }
}
