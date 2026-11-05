// The wire shapes of the model routes (docs/specs/gateway.md section 3.8), as
// `apps/api/src/localai/gateway/policies/policies.schemas.ts` returns them.

export type ModelLiveState = 'loaded' | 'loadable' | 'unloaded' | 'none';

export interface ModelPolicy {
  enabled: boolean;
  aliases: string[];
  onDemandLoad: boolean;
  /** Seconds a request waits for an on-demand load (10 to 600). */
  loadWaitS: number;
  /** null = the setting `gateway.timeouts.firstByteMs`. */
  firstByteTimeoutS: number | null;
  /** Node ids in order of preference. */
  preferredNodes: string[];
  /** False while the model has no stored row (the defaults apply). */
  stored: boolean;
  updatedAt: string | null;
}

export interface ModelNode {
  nodeId: string;
  status: 'loaded' | 'loadable';
}

export interface ModelRow {
  modelId: string;
  displayName: string;
  roles: string[];
  policy: ModelPolicy;
  state: ModelLiveState;
  /** Whether `/v1/models` lists it for a key with no scope. */
  listed: boolean;
  nodes: ModelNode[];
}

/** The body of `PUT /api/localai/gateway/models/:modelId`. */
export interface PolicyInput {
  enabled: boolean;
  aliases: string[];
  onDemandLoad: boolean;
  loadWaitS: number;
  firstByteTimeoutS: number | null;
  preferredNodes: string[];
}
