// One line per audit row and the page it links to (docs/specs/cloud-app.md sections 3.5 and 3.9). Pure: no I/O.
//
// SAFETY. `audit_events.meta` is written by many callers, and the rule for them is "scalar, never a secret". This file
// does not rely on that: every field is read through a WHITELIST (a named field, a strict pattern for its value, a
// length cap), and whatever passes is run through the same scrubber the command plane uses (`scrubText`: tokens,
// 64-hex keys, tailnet addresses). A field that is not whitelisted is never printed, and a value that the scrubber
// would change is dropped rather than shown redacted. An action this file does not know renders as its own name.
import { scrubText } from '../commands/command-scrub';
import {
  AUDIT_TARGET_ALERT,
  AUDIT_TARGET_CATALOG,
  AUDIT_TARGET_COMMAND,
  AUDIT_TARGET_NODE,
  NODE_ACTOR_ACTIONS,
} from './audit-actions';

export interface AuditRowSource {
  action: string;
  targetType: string;
  targetId: string;
  meta: unknown;
}

/** Lab id lookup for a `localai_nodes.id` uuid (the code writes the lab id `ai-n3`, the spec's wording allows a uuid). */
export type NodeLabIds = ReadonlyMap<string, string>;

export interface AuditDescription {
  summary: string;
  /** An in-app path, or null when the row has no page to open. */
  link: string | null;
  /** The lab node id (`ai-n3`) the row concerns, when it concerns one. */
  nodeId: string | null;
}

export interface AuditActor {
  kind: 'user' | 'node' | 'system';
  label: string;
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const NODE_ID = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;
const WORD = /^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$/;
const COMMAND_TYPE = /^(?:localai\.)?[a-z][a-z_]{0,31}\.[a-z][a-z_]{0,31}$/;
const VERSION = /^[0-9A-Za-z][0-9A-Za-z._+-]{0,39}$/;
const SOURCE_REF = /^[A-Za-z0-9][A-Za-z0-9._/@+-]{0,79}$/;
const TOKEN_PREFIX = /^(?:nod|pat|lat|lai)_[A-Za-z0-9]{0,12}$/;
const KEY_HINT = /^[0-9a-f]{4}…[0-9a-f]{4}$/;
const ERROR_CODE = /^LAI-[A-Z]{2,4}-\d{3}$/;
const STEP = /^[a-z][a-z0-9_]{0,31}$/;
const ALERT_KIND = /^[a-z][a-z0-9_.]{0,47}$/;
const MODES = new Set(['online', 'offline', 'survival']);
const COMMAND_STATUSES = new Set(['queued', 'running', 'succeeded', 'failed', 'expired', 'cancelled']);
const KEY_REASONS = new Set(['enroll', 'rotate', 'refresh']);
const CATALOG_SOURCES = new Set(['github_release', 'upload', 'editor']);
/** The node fields whose NAMES an update row may list. Values are never printed (the tailnet IP is a field here). */
const NODE_FIELDS = new Set(['displayName', 'publicHost', 'servePattern', 'tailnetMachine', 'tailnetIp', 'gatewayEnabled', 'gatewayPriority', 'catalogPin']);
/** The params of a command that name its subject, in preference order. */
const SUBJECT_PARAMS = ['model_id', 'service', 'unit'] as const;

const isRecord = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === 'object' && !Array.isArray(value);

/** A whitelisted string: right type, within `max`, matching `pattern`, and untouched by the scrubber. Else null. */
function safe(value: unknown, pattern: RegExp, max = 80): string | null {
  if (typeof value !== 'string' || value.length === 0 || value.length > max || !pattern.test(value)) return null;
  return scrubText(value) === value ? value : null;
}

/** Free text a person typed (a gateway key name): control characters out, capped, scrubbed. */
function freeText(value: unknown, max = 60): string | null {
  if (typeof value !== 'string') return null;
  // eslint-disable-next-line no-control-regex
  const cleaned = value.replace(/[\u0000-\u001f\u007f]/g, ' ').replace(/\s+/g, ' ').trim();
  if (cleaned.length === 0) return null;
  const clipped = cleaned.length > max ? `${cleaned.slice(0, max - 1)}…` : cleaned;
  return scrubText(clipped);
}

function labId(value: unknown, ids: NodeLabIds): string | null {
  if (typeof value !== 'string') return null;
  if (UUID.test(value)) return ids.get(value) ?? ids.get(value.toLowerCase()) ?? null;
  return safe(value, NODE_ID, 64);
}

/** `localai.model.load` -> `model.load`. */
function commandType(value: unknown): string | null {
  const type = safe(value, COMMAND_TYPE, 70);
  return type === null ? null : type.replace(/^localai\./, '');
}

/** The subject of a command (its model or unit) from `params`, which is a JSON string in an audit row. */
function commandSubject(params: unknown): string | null {
  let parsed: unknown = params;
  if (typeof params === 'string') {
    if (params.length > 4096) return null;
    try {
      parsed = JSON.parse(params);
    } catch {
      return null;
    }
  }
  if (!isRecord(parsed)) return null;
  for (const key of SUBJECT_PARAMS) {
    const subject = safe(parsed[key], WORD, 64);
    if (subject !== null) return subject;
  }
  return null;
}

/** The names (never the values) of the node fields a row says changed. */
function changedFields(meta: Record<string, unknown>): string[] {
  const found = new Set<string>();
  for (const key of Object.keys(meta)) if (NODE_FIELDS.has(key)) found.add(key);
  for (const holder of [meta.fields, meta.changed]) {
    const names = Array.isArray(holder) ? holder : typeof holder === 'string' ? holder.split(',') : [];
    for (const name of names) if (typeof name === 'string' && NODE_FIELDS.has(name.trim())) found.add(name.trim());
  }
  return [...found];
}

const join = (parts: Array<string | null>): string => parts.filter((part): part is string => part !== null && part !== '').join(' ');
const on = (node: string | null): string | null => (node === null ? null : `on ${node}`);

/** The lab node id a row concerns. */
function rowNode(src: AuditRowSource, meta: Record<string, unknown>, ids: NodeLabIds): string | null {
  if (src.action === 'localai.node.binding_refused') return labId(meta.claimedNodeId, ids) ?? labId(src.targetId, ids);
  if (src.targetType === AUDIT_TARGET_NODE) return labId(src.targetId, ids);
  if (src.targetType === AUDIT_TARGET_COMMAND || src.targetType === AUDIT_TARGET_ALERT) return labId(meta.nodeId, ids);
  return null;
}

function linkOf(src: AuditRowSource, node: string | null): string | null {
  if (src.action.startsWith('localai.gateway.')) return '/gateway/keys';
  if (src.targetType === AUDIT_TARGET_NODE) return node === null ? null : `/lab/nodes/${encodeURIComponent(node)}`;
  if (src.targetType === AUDIT_TARGET_COMMAND) {
    if (node === null || !UUID.test(src.targetId)) return null;
    return `/lab/nodes/${encodeURIComponent(node)}?command=${src.targetId}`;
  }
  if (src.targetType === AUDIT_TARGET_CATALOG) {
    const version = safe(src.targetId, VERSION, 40);
    return version === null ? '/lab/catalog' : `/lab/catalog?version=${encodeURIComponent(version)}`;
  }
  if (src.targetType === AUDIT_TARGET_ALERT) return '/lab/alerts';
  return null;
}

function summaryOf(src: AuditRowSource, meta: Record<string, unknown>, node: string | null, ids: NodeLabIds): string {
  const nodeLabel = node === null ? 'a node' : node;
  switch (src.action) {
    case 'localai.node.created':
      return `Added node ${nodeLabel}`;
    case 'localai.node.updated': {
      const fields = changedFields(meta);
      return fields.length > 0 ? `Changed ${fields.join(', ')} on ${nodeLabel}` : `Changed settings of ${nodeLabel}`;
    }
    case 'localai.node.retired':
      return `Retired node ${nodeLabel}`;
    case 'localai.node.credential_minted':
      return join(['Created credential', safe(meta.tokenPrefix, TOKEN_PREFIX, 20), 'for', nodeLabel]);
    case 'localai.node.credential_revoked':
      return join(['Revoked credential', safe(meta.tokenPrefix, TOKEN_PREFIX, 20), 'of', nodeLabel]);
    case 'localai.node.enrolled': {
      const version = safe(meta.agentVersion, VERSION, 32);
      return version === null ? `${nodeLabel} enrolled` : `${nodeLabel} enrolled with agent ${version}`;
    }
    case 'localai.node.binding_refused':
      return join(['Refused credential', safe(meta.credentialPrefix, TOKEN_PREFIX, 20), 'claiming', nodeLabel]);
    case 'localai.node.mode_changed': {
      const from = typeof meta.from === 'string' && MODES.has(meta.from) ? meta.from : null;
      const to = typeof meta.to === 'string' && MODES.has(meta.to) ? meta.to : null;
      if (from !== null && to !== null) return `${nodeLabel} mode changed from ${from} to ${to}`;
      return to !== null ? `${nodeLabel} mode changed to ${to}` : `${nodeLabel} mode changed`;
    }
    case 'localai.node.tailnet_revealed':
      return `Tailnet address of ${nodeLabel} revealed`;
    case 'localai.node.key_reported': {
      const reason = typeof meta.reason === 'string' && KEY_REASONS.has(meta.reason) ? meta.reason : null;
      const hint = safe(meta.keyHint, KEY_HINT, 12);
      return join([`${nodeLabel} reported its key`, reason === null ? null : `(${reason})`, hint === null ? null : `hint ${hint}`]);
    }
    case 'localai.command.requested':
    case 'localai.command.cancelled': {
      const verb = src.action === 'localai.command.cancelled' ? 'Cancelled ' : '';
      return join([verb + (commandType(meta.type) ?? 'command'), commandSubject(meta.params), on(node)]);
    }
    case 'localai.command.settled': {
      const status = typeof meta.status === 'string' && COMMAND_STATUSES.has(meta.status) ? meta.status : null;
      const code = safe(meta.errorCode, ERROR_CODE, 16);
      return join([commandType(meta.type) ?? 'command', on(node), status === null ? 'finished' : status, code === null ? null : `(${code})`]);
    }
    case 'localai.catalog.imported': {
      const source = typeof meta.source === 'string' && CATALOG_SOURCES.has(meta.source) ? meta.source : null;
      const ref = safe(meta.sourceRef, SOURCE_REF, 80);
      return join([`Imported catalog ${versionOf(src)}`, source === null ? null : `from ${source}`, ref === null ? null : `(${ref})`]);
    }
    case 'localai.catalog.linted':
      return `Catalog ${versionOf(src)} ${meta.valid === true ? 'passed the check' : meta.valid === false ? 'failed the check' : 'was checked'}`;
    case 'localai.catalog.approved': {
      const notified = typeof meta.nodesNotified === 'number' && Number.isInteger(meta.nodesNotified) && meta.nodesNotified >= 0 ? meta.nodesNotified : null;
      const tail = notified === null || notified === 0 ? '' : `, ${notified} ${notified === 1 ? 'node' : 'nodes'} told to refresh`;
      return `Approved catalog ${versionOf(src)}${tail}`;
    }
    case 'localai.checklist.updated': {
      const step = safe(meta.step, STEP, 32);
      const state = safe(meta.state, STEP, 32);
      return join(['Checklist step', step, 'of', nodeLabel, state === null ? 'updated' : `is now ${state}`]);
    }
    case 'localai.alert.acknowledged': {
      const kind = safe(meta.kind, ALERT_KIND, 48);
      return join(['Acknowledged', kind, 'alert', on(labId(meta.nodeId, ids))]);
    }
    case 'localai.gateway.key_created':
    case 'localai.gateway.key_revoked':
    case 'localai.gateway.key_updated': {
      const verb = src.action.endsWith('created') ? 'created' : src.action.endsWith('revoked') ? 'revoked' : 'changed';
      const name = freeText(meta.name);
      return join(['Gateway key', safe(meta.prefix, TOKEN_PREFIX, 20) ?? safe(meta.prefix, WORD, 20), name === null ? null : `"${name}"`, verb]);
    }
    case 'localai.gateway.policy_updated':
      return 'Gateway model policy changed';
    default:
      // A new or unknown action never breaks the page: its name is the summary.
      return scrubText(src.action.slice(0, 120));
  }
}

function versionOf(src: AuditRowSource): string {
  return safe(src.targetId, VERSION, 40) ?? 'version';
}

/** The summary, link and node of one audit row. */
export function describeAudit(src: AuditRowSource, ids: NodeLabIds = new Map()): AuditDescription {
  const meta = isRecord(src.meta) ? src.meta : {};
  const nodeId = rowNode(src, meta, ids);
  return { summary: summaryOf(src, meta, nodeId, ids), link: linkOf(src, nodeId), nodeId };
}

/**
 * The actor of a row. A node credential's actions show "node ai-n3" (the stored actor is the credential's owner, but
 * the node acted); a row with no stored user is "system" (a job); anything else is the user's email.
 */
export function auditActor(action: string, nodeId: string | null, userEmail: string | null): AuditActor {
  if (NODE_ACTOR_ACTIONS.has(action) && nodeId !== null) return { kind: 'node', label: `node ${nodeId}` };
  if (userEmail !== null) return { kind: 'user', label: userEmail };
  return { kind: 'system', label: 'system' };
}
