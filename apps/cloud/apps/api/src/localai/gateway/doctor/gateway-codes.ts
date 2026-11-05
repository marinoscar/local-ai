// The four doctor codes the gateway's Doctor checks answer with (docs/specs/gateway.md sections 7 and 8, registry
// docs/specs/doctor-codes.md area GW). Same shape as the cloud's `CLOUD_CODES` so `buildOutcome` takes them; the wording
// is the registry's, word for word (`gateway-codes.spec.ts` compares). A code string is never invented here.
import type { CloudCode } from '../../doctor/codes';

export const GATEWAY_CODES = {
  /** The gateway is switched off (`LOCALAI_GATEWAY_ENABLED=false`) or the site is in maintenance. */
  unavailable: {
    code: 'LAI-GW-051',
    severity: 'warning',
    title: 'The gateway is turned off or the site is in maintenance',
    fix: 'Try later; admin: check `LOCALAI_GATEWAY_ENABLED` and maintenance mode',
  },
  /** No node can serve any model right now. */
  noCapacity: {
    code: 'LAI-GW-021',
    severity: 'error',
    title: 'No lab computer can run that model right now',
    fix: 'Try later, pick another model, or ask the admin to load it',
  },
  /** The cloud's copy of a node's llama key was refused by the node. */
  nodeKeyInvalid: {
    code: 'LAI-GW-043',
    severity: 'critical',
    title: "The cloud's copy of a node's API key is wrong",
    fix: 'Admin: press "Refresh status" on that node, or run `sudo aictl cloud resend-key` on it',
  },
  /** Usage rows were dropped because the database refused them. */
  usageDropped: {
    code: 'LAI-GW-060',
    severity: 'warning',
    title: "Some usage records couldn't be saved",
    fix: 'Admin: check the database; requests themselves were not affected',
  },
} as const satisfies Record<string, CloudCode>;
