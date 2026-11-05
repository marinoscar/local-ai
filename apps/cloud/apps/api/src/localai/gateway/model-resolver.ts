// The seam between the proxy and "which model is this, may this key use it" (docs/specs/gateway.md sections 4.1, 4.3 and
// 4.4). The proxy only needs the answer; HOW it is computed (aliases, the policy table, the approved catalog, the scope
// rule) is `models/resolve.ts` (GW-05), bound to this port by `model-resolver.catalog.ts`.
import type { GatewayKeyRecord } from './auth';
import type { RoutingPolicy } from './routing/routing.types';

/** The part of `localai_model_policies` the proxy reads. A model with no policy row has the defaults. */
export interface ProxyModelPolicy extends RoutingPolicy {
  onDemandLoad: boolean;
  loadWaitS: number;
  /** `policy.firstByteTimeoutS`: overrides `gateway.timeouts.firstByteMs` for this model. */
  firstByteTimeoutS: number | null;
}

export const DEFAULT_MODEL_POLICY: ProxyModelPolicy = { preferredNodes: [], onDemandLoad: false, loadWaitS: 120, firstByteTimeoutS: null };

export interface ResolvedGatewayModel {
  /** The catalog id; the value that replaces `model` in the upstream body. */
  modelId: string;
  /** As the client sent it (an id or an alias). */
  requestedModel: string;
  policy: ProxyModelPolicy;
  /** The roles nodes report for the model (`embedding`, `chat`, ...); null when unknown. */
  roles: readonly string[] | null;
  /** Step 4 of the selection: is the model in the approved catalog (503 LAI-GW-021) or unknown (404 LAI-GW-020)? */
  inApprovedCatalog: boolean;
  /**
   * The display prefix (`lai_` + 6 characters) of the key that asked: the on-demand load (GW-09) records it in the audit
   * row of the command it queues. Never the token. Absent when the resolver was not given a key.
   */
  keyPrefix?: string;
}

export interface GatewayModelResolver {
  /**
   * @throws GatewayError 404 LAI-GW-020 (unknown model, or its policy is disabled) or 403 LAI-GW-012 (outside the key's scope).
   */
  resolve(requested: string, key: GatewayKeyRecord): Promise<ResolvedGatewayModel>;
}

/** Injection token of the {@link GatewayModelResolver}. */
export const GATEWAY_MODEL_RESOLVER = Symbol('LOCALAI_GATEWAY_MODEL_RESOLVER');
