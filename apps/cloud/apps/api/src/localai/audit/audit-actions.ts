// The audit actions of the Local AI cloud (docs/specs/cloud-app.md section 3.5), declared ONCE. The audit page's
// action filter, the summary formatter and the tests read this list, so a new action is one entry here (and one
// case in `audit-summary.ts`; an action that has none still renders, with its name as the summary). The action
// strings are permanent: rows are stored under them. An action written by a later item (tailnet_revealed,
// the gateway actions) is listed already so the filter offers it the day its first row lands.

/** Every `audit_events.action` this page lists starts with this. */
export const AUDIT_ACTION_PREFIX = 'localai.';

export interface AuditGroup {
  /** The prefix filter for the group (`?action=localai.command.`). */
  readonly prefix: string;
  readonly label: string;
}

/** The filter dropdown's groups, in display order. Each is a prefix of the actions below. */
export const AUDIT_GROUPS: readonly AuditGroup[] = [
  { prefix: 'localai.node.', label: 'Nodes' },
  { prefix: 'localai.command.', label: 'Commands' },
  { prefix: 'localai.catalog.', label: 'Catalog' },
  { prefix: 'localai.checklist.', label: 'Publish checklist' },
  { prefix: 'localai.alert.', label: 'Alerts' },
  { prefix: 'localai.gateway.', label: 'Gateway' },
];

export interface AuditActionDef {
  readonly action: string;
  /** Plain-language name for the filter dropdown. */
  readonly label: string;
}

/** The known actions. The order is the dropdown order. */
export const KNOWN_AUDIT_ACTIONS: readonly AuditActionDef[] = [
  { action: 'localai.node.created', label: 'Node added' },
  { action: 'localai.node.updated', label: 'Node changed' },
  { action: 'localai.node.retired', label: 'Node retired' },
  { action: 'localai.node.credential_minted', label: 'Credential created' },
  { action: 'localai.node.credential_revoked', label: 'Credential revoked' },
  { action: 'localai.node.enrolled', label: 'Node enrolled' },
  { action: 'localai.node.binding_refused', label: 'Credential refused' },
  { action: 'localai.node.mode_changed', label: 'Mode changed' },
  { action: 'localai.node.tailnet_revealed', label: 'Tailnet address revealed' },
  { action: 'localai.node.key_reported', label: 'Node key reported' },
  { action: 'localai.command.requested', label: 'Command requested' },
  { action: 'localai.command.cancelled', label: 'Command cancelled' },
  { action: 'localai.command.settled', label: 'Command finished' },
  { action: 'localai.catalog.imported', label: 'Catalog imported' },
  { action: 'localai.catalog.linted', label: 'Catalog checked' },
  { action: 'localai.catalog.approved', label: 'Catalog approved' },
  { action: 'localai.checklist.updated', label: 'Checklist step updated' },
  { action: 'localai.alert.acknowledged', label: 'Alert acknowledged' },
  { action: 'localai.gateway.key_created', label: 'Gateway key created' },
  { action: 'localai.gateway.key_revoked', label: 'Gateway key revoked' },
  { action: 'localai.gateway.key_updated', label: 'Gateway key changed' },
  { action: 'localai.gateway.policy_updated', label: 'Gateway policy changed' },
];

/**
 * Actions a node's own credential triggers. Their `actorUserId` is the credential's OWNER (the user who made it), but
 * the one acting is the node, so the page labels the actor "node ai-n3" rather than that person's email.
 */
export const NODE_ACTOR_ACTIONS: ReadonlySet<string> = new Set([
  'localai.node.enrolled',
  'localai.node.binding_refused',
  'localai.node.mode_changed',
  'localai.node.key_reported',
]);

/** The `targetType`s the cloud writes. */
export const AUDIT_TARGET_NODE = 'localai_node';
export const AUDIT_TARGET_COMMAND = 'localai_command';
export const AUDIT_TARGET_CATALOG = 'localai_catalog';
export const AUDIT_TARGET_ALERT = 'localai_alert';
