// Workload types of the E11 contract (agent `GET /api/v1/workloads`, docs/specs/agent.md section 3.8) and the
// transport the WorkloadSwitcher talks through. The agent UI implements it over its REST API and SSE stream; the cloud
// over `/api/localai/nodes/:nodeId/workloads/:name/activate`. The component never fetches by itself.

/** One named workload: a set of models that run together. */
export interface Workload {
  name: string;
  label: { en: string; es: string };
  models: string[];
  /** True when every model of the set is loaded right now. */
  active: boolean;
  /** False when the set does not fit this node's GPU (activation answers 422 LAI-LLM-051). */
  fits: boolean;
  /** Model ids of the set that are not on this node. */
  missing: string[];
}

/** `GET /workloads`. */
export interface WorkloadsResponse {
  workloads: Workload[];
  /** The name of the active workload, or null when none is complete. */
  active: string | null;
}

/** One step of an activation (SSE `workload.progress`, or what a polling transport makes up). */
export interface WorkloadProgress {
  /** 0..1, or null when the step has no measure. */
  progress: number | null;
  /** A short machine word for the step (`unloading`, `loading`), shown only as a hint. */
  step?: string | null;
  /** The model being loaded or unloaded, when known. */
  model?: string | null;
}

export interface ActivateOptions {
  /** Switch even in survival mode (409 LAI-LLM-053 otherwise). Only shown when the host allows it. */
  force?: boolean;
  /** Called with each progress step while the activation runs. */
  onProgress?: (p: WorkloadProgress) => void;
  /** Stops listening (the screen was left). It does not cancel the activation on the node. */
  signal?: AbortSignal;
}

/**
 * WorkloadTransport is how the switcher reaches a node.
 *
 * `activate` resolves when the workload is active and rejects with a TransportError (doctor code, message, fix) when
 * the node refused it or it failed (the node restored the previous set: LAI-LLM-052).
 */
export interface WorkloadTransport {
  list(): Promise<WorkloadsResponse>;
  activate(name: string, opts?: ActivateOptions): Promise<void>;
}
