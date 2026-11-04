// The `localai.node.key_reported` event (docs/specs/cloud-app.md sections 3.8 and 7), published on the platform's
// `EVENT_BUS` after a node's llama key was stored. The gateway drops its cached key for the node and clears its
// in-memory `keyInvalid` mark when it sees it. The payload NEVER carries the key.

/** The permanent channel name. */
export const LOCALAI_NODE_KEY_REPORTED_EVENT = 'localai.node.key_reported';

/** Why a key was reported, derived server-side (spec section 3.5 audit table). */
export type KeyReportReason = 'enroll' | 'rotate' | 'refresh';

export interface NodeKeyReportedEvent {
  /** The lab node id, `ai-n<N>`. */
  nodeId: string;
  /** `first4…last4` of the stored key. */
  keyHint: string;
  reason: KeyReportReason;
}

/** `enroll` when the cloud held no key, `rotate` when the hint changed, `refresh` for the same key again. */
export function keyReportReason(previousHint: string | null, newHint: string): KeyReportReason {
  if (previousHint === null) return 'enroll';
  return previousHint === newHint ? 'refresh' : 'rotate';
}
