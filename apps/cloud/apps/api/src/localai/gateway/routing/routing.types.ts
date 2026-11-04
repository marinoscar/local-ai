// The vocabulary of the gateway routing table (docs/specs/gateway.md section 4.3). Types and constants only.
//
// A CANDIDATE is a (node, model) pair the gateway may send a request to. The static part is rebuilt from the database;
// the live part (inflight, suspect, keyInvalid, ewma) is kept apart by the table so a rebuild never resets it, and is
// copied into a `SelectedCandidate` when the selection hands a candidate out.

/** `loaded`: the node's last snapshot says the model is loaded. `loadable`: present, verified, loadable and unloaded. */
export type CandidateStatus = 'loaded' | 'loadable';

/** What identifies a candidate to the hooks of the attempt loop. */
export interface CandidateRef {
  nodeId: string;
  modelId: string;
}

/**
 * The static part of a candidate. `tailnetIp` stays in memory only: it is never logged, never exported to `/v1/models`
 * and never sent to the UI (gateway.md section 4.3 note 3).
 */
export interface Candidate extends CandidateRef {
  publicHost: string;
  servePattern: 'A' | 'B';
  tailnetIp: string | null;
  tailnetMachine: string | null;
  /** `gateway_priority`: lower is preferred. */
  priority: number;
  status: CandidateStatus;
  /** Present, verified and loadable: a model that was marked unloaded can be loaded again (on demand). */
  canLoad: boolean;
  /** Parallel requests the model serves at once on this node (llama `--parallel`); at least 1. */
  slots: number;
  ctx: number | null;
}

/** A candidate with its live counters at the moment of the selection (the shape of gateway.md section 4.3). */
export interface SelectedCandidate extends Candidate {
  inflight: number;
  /** Epoch ms until which the node is suspect (0 = not suspect by an attempt failure). */
  suspectUntil: number;
  /** Null until a first byte was measured. */
  ewmaTtfbMs: number | null;
}

/** The part of a model policy the selection reads. */
export interface RoutingPolicy {
  /** Node ids in order of preference; a node that is absent sorts after every listed one. */
  preferredNodes: readonly string[];
}

export const NO_POLICY: RoutingPolicy = { preferredNodes: [] };

/** The outcome of steps 1 and 2 of the selection. */
export type SelectResult =
  /** Candidates to try, in order. Saturated ones are already left out. Never empty. */
  | { kind: 'candidates'; candidates: SelectedCandidate[] }
  /** Loaded candidates exist but every one is at `queueFactor x slots`: 503 LAI-GW-023. */
  | { kind: 'saturated' }
  /** No loaded, healthy, untried candidate: the caller goes on to the on-demand path or to {@link unavailableOutcome}. */
  | { kind: 'none_loaded' };

/** The error the gateway answers when the selection finds nothing (step 4). */
export interface UnavailableOutcome {
  status: 404 | 503;
  code: 'LAI-GW-020' | 'LAI-GW-021';
}

export const GW_UNKNOWN_MODEL = 'LAI-GW-020';
export const GW_NO_CAPACITY = 'LAI-GW-021';
export const GW_ALL_BUSY = 'LAI-GW-023';
export const GW_KEYS_UNREADABLE = 'LAI-GW-050';

/** A node of the table with what the load primitives need. */
export interface RoutableNode {
  nodeId: string;
  publicHost: string;
}

/** A candidate that is not loaded but may be (the on-demand path of GW-09). */
export interface LoadableCandidate extends SelectedCandidate {
  /** Models of the node whose status is `loaded`, each with its live inflight count (never evict a busy one). */
  loadedOnNode: ReadonlyArray<{ modelId: string; inflight: number }>;
}

/** The settings of the table (`gateway.queueFactor`, gateway.md section 3.7). */
export interface RoutingSettings {
  queueFactor: number;
}

export const DEFAULT_QUEUE_FACTOR = 4;
export const SUSPECT_MS = 30_000;
export const EWMA_ALPHA = 0.2;
export const REBUILD_INTERVAL_MS = 10_000;
export const PROBE_INTERVAL_MS = 30_000;
export const PROBE_TIMEOUT_MS = 3_000;
export const PROBE_FAILURES_TO_DOWN = 3;
