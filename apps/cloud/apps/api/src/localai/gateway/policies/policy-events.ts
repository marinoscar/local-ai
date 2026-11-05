// The in-process event a policy write publishes, AFTER the write committed (docs/specs/gateway.md section 3.8). The
// listeners drop the caches that hold policies, so `/v1/models` and the model resolution see the write within one
// request instead of one cache lifetime, on every replica that shares the bus.
export const GATEWAY_POLICY_UPDATED_CHANNEL = 'localai.gateway.policy.updated';

export interface GatewayPolicyUpdatedEvent {
  modelId: string;
}
