// The seam for the per-key limits (GW-08): RPM window, concurrency semaphore, daily token budget
// (docs/specs/gateway.md section 4.1). GW-06 only defines WHEN they run: after the model is resolved and the scope is
// checked, before a node is selected. `admit` throws a `GatewayError` (LAI-GW-030/031/032) to refuse the request;
// otherwise it returns a ticket that is settled exactly once when the response has ended (finished, failed or aborted),
// which is where the concurrency slot is released and the tokens are counted.
import type { GatewayRequestContext } from './routes';

export interface AdmissionInput {
  ctx: GatewayRequestContext;
  /** The resolved catalog id. */
  modelId: string;
  endpoint: string;
  stream: boolean;
}

export interface SettleInfo {
  promptTokens: number | null;
  completionTokens: number | null;
  aborted: boolean;
}

export interface AdmissionTicket {
  /** Headers added to the response (`x-ratelimit-*`), on success and on a gateway error. */
  headers(): Record<string, string>;
  /** Called once per admitted request when it ends. Must not throw. */
  settle(info: SettleInfo): void;
}

export interface GatewayLimits {
  admit(input: AdmissionInput): Promise<AdmissionTicket>;
}

/** Injection token of the {@link GatewayLimits}. */
export const GATEWAY_LIMITS = Symbol('LOCALAI_GATEWAY_LIMITS');

const NO_TICKET: AdmissionTicket = { headers: () => ({}), settle: () => undefined };

/** Until GW-08: everything is admitted and nothing is counted. */
export const noopLimits: GatewayLimits = { admit: async () => NO_TICKET };
