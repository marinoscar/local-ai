// How a model's state and policy read in the table (the wireframe of gateway.md section 3.8). Pure.
import type { ModelLiveState, ModelRow, PolicyInput } from './modelsTypes';

export interface StateView {
  /** The word next to the dot. */
  label: string;
  /** A plain-language sentence for the tooltip and the screen reader. */
  hint: string;
  color: 'success' | 'info' | 'warning' | 'default';
}

const STATES: Record<ModelLiveState, StateView> = {
  loaded: { label: 'loaded', hint: 'A node has it loaded and answers requests now.', color: 'success' },
  loadable: { label: 'loadable', hint: 'No node has it loaded; a request loads it on demand.', color: 'info' },
  unloaded: { label: 'unloaded', hint: 'A node has it but it is not offered on /v1 (on-demand load is off or the model is disabled).', color: 'warning' },
  none: { label: 'no node', hint: 'No node that can serve requests has this model.', color: 'default' },
};

export const describeState = (state: ModelLiveState): StateView => STATES[state];

/** "ai-n1" or "ai-n1, ai-n2" (loaded nodes first, as the API sends them); an em dash when none. */
export function describeNodes(row: Pick<ModelRow, 'nodes'>): string {
  return row.nodes.length === 0 ? '—' : row.nodes.map((node) => node.nodeId).join(', ');
}

/** "lab-coder, short"; an em dash when there is none. */
export const describeAliases = (aliases: readonly string[]): string => (aliases.length === 0 ? '—' : aliases.join(', '));

/** "off", or "on · wait 120 s". */
export const describeOnDemand = (policy: Pick<ModelRow['policy'], 'onDemandLoad' | 'loadWaitS'>): string => (policy.onDemandLoad ? `on · wait ${policy.loadWaitS} s` : 'off');

export const describePreferred = (nodes: readonly string[]): string => (nodes.length === 0 ? '—' : nodes.join(' › '));

export function inputOf(row: ModelRow): PolicyInput {
  const { policy } = row;
  return {
    enabled: policy.enabled,
    aliases: [...policy.aliases],
    onDemandLoad: policy.onDemandLoad,
    loadWaitS: policy.loadWaitS,
    firstByteTimeoutS: policy.firstByteTimeoutS,
    preferredNodes: [...policy.preferredNodes],
  };
}

const ALIAS = /^[a-z0-9][a-z0-9.-]{2,63}$/;
const NODE = /^ai-n[1-9][0-9]{0,3}$/;

/** Splits a comma/space/newline separated text into trimmed, non-empty, de-duplicated entries (first one wins). */
export function splitList(text: string): string[] {
  return [...new Set(text.split(/[\s,]+/).map((entry) => entry.trim()).filter((entry) => entry !== ''))];
}

export interface PolicyDraft {
  enabled: boolean;
  aliases: string;
  onDemandLoad: boolean;
  loadWaitS: string;
  firstByteTimeoutS: string;
  preferredNodes: string;
}

export const draftOf = (row: ModelRow): PolicyDraft => ({
  enabled: row.policy.enabled,
  aliases: row.policy.aliases.join(', '),
  onDemandLoad: row.policy.onDemandLoad,
  loadWaitS: String(row.policy.loadWaitS),
  firstByteTimeoutS: row.policy.firstByteTimeoutS === null ? '' : String(row.policy.firstByteTimeoutS),
  preferredNodes: row.policy.preferredNodes.join(', '),
});

export interface DraftCheck {
  input: PolicyInput | null;
  errors: Partial<Record<'aliases' | 'loadWaitS' | 'firstByteTimeoutS' | 'preferredNodes', string>>;
}

/** The checks the API repeats (policies.schemas.ts), so the form says what is wrong before it sends. */
export function checkDraft(draft: PolicyDraft, catalogIds: readonly string[]): DraftCheck {
  const errors: DraftCheck['errors'] = {};
  const aliases = splitList(draft.aliases);
  const badAlias = aliases.find((alias) => !ALIAS.test(alias));
  if (badAlias) errors.aliases = `"${badAlias}" is not a valid alias: 3 to 64 lowercase letters, digits, dots or dashes.`;
  else if (aliases.length > 16) errors.aliases = 'At most 16 aliases.';
  else {
    const clash = aliases.find((alias) => catalogIds.includes(alias));
    if (clash) errors.aliases = `"${clash}" is already the id of a model.`;
  }
  const nodes = splitList(draft.preferredNodes);
  const badNode = nodes.find((node) => !NODE.test(node));
  if (badNode) errors.preferredNodes = `"${badNode}" is not a node id such as ai-n1.`;

  const wait = Number(draft.loadWaitS);
  if (draft.loadWaitS.trim() === '' || !Number.isInteger(wait) || wait < 10 || wait > 600) errors.loadWaitS = 'A whole number of seconds from 10 to 600.';
  let firstByte: number | null = null;
  if (draft.firstByteTimeoutS.trim() !== '') {
    firstByte = Number(draft.firstByteTimeoutS);
    if (!Number.isInteger(firstByte) || firstByte < 1 || firstByte > 3600) errors.firstByteTimeoutS = 'A whole number of seconds from 1 to 3600, or empty for the default.';
  }
  if (Object.keys(errors).length > 0) return { input: null, errors };
  return { input: { enabled: draft.enabled, aliases, onDemandLoad: draft.onDemandLoad, loadWaitS: wait, firstByteTimeoutS: firstByte, preferredNodes: nodes }, errors };
}
