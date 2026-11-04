// The audit actions and target types of the gateway (docs/specs/gateway.md section 3.6), declared ONCE. The audit
// page's action filter (`audit/audit-actions.ts`) reads these, so the strings cannot drift. The action strings are
// permanent: `audit_events` rows are stored under them.
//
// What a row's `meta` may carry (the writers are GW-02 for keys and GW-09 for policies):
//   - key_created / key_revoked / key_updated: `prefix` (the display prefix, `lai_` + 6 characters), `name`, the limits
//     and `allowedModels`. NEVER the token and NEVER `tokenHash`.
//   - policy_updated: the changed policy fields of the model named by the row's `targetId`.

/** A gateway key was created (the token was shown once). */
export const AUDIT_GATEWAY_KEY_CREATED = 'localai.gateway.key_created';
/** A gateway key was revoked. */
export const AUDIT_GATEWAY_KEY_REVOKED = 'localai.gateway.key_revoked';
/** A gateway key's name, limits or allowed models were changed. */
export const AUDIT_GATEWAY_KEY_UPDATED = 'localai.gateway.key_updated';
/** A model's routing policy was changed. */
export const AUDIT_GATEWAY_POLICY_UPDATED = 'localai.gateway.policy_updated';

/** Every gateway audit action, in the order the audit page's filter lists them. */
export const GATEWAY_AUDIT_ACTIONS = [AUDIT_GATEWAY_KEY_CREATED, AUDIT_GATEWAY_KEY_REVOKED, AUDIT_GATEWAY_KEY_UPDATED, AUDIT_GATEWAY_POLICY_UPDATED] as const;

export type GatewayAuditAction = (typeof GATEWAY_AUDIT_ACTIONS)[number];

/** The `audit_events.targetType` of a key row (`targetId` is the key id) and of a policy row (`targetId` is the model id). */
export const AUDIT_TARGET_GATEWAY_KEY = 'localai_gateway_key';
export const AUDIT_TARGET_MODEL_POLICY = 'localai_model_policy';
