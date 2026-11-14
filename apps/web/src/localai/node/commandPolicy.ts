// Who may send which command and when a button is disabled (docs/specs/cloud-app.md sections 3.7 and 3.9, issue #86).
// The server decides again on every request; this is only so the page never offers what would be refused. The
// permission of a type is the policy table's: the four admin types need lab:admin, the other ten lab:operate, and
// lab:admin does not stand in for lab:operate.
import type { NodeDetail } from '../api/types';

/** Types that need lab:admin (`commands/command-policy.ts` on the API). */
export const ADMIN_COMMAND_TYPES: ReadonlySet<string> = new Set(['model.delete', 'mode.offline', 'key.rotate', 'agent.update']);

/** The E11 types, which need their own permission (`localai_workloads:write`, `localai_music:use`). */
const FEATURE_PERMISSIONS: Readonly<Record<string, string>> = { 'workload.activate': 'localai_workloads:write', 'music.generate': 'localai_music:use' };

export const requiredPermission = (type: string): string => FEATURE_PERMISSIONS[stripPrefix(type)] ?? (ADMIN_COMMAND_TYPES.has(stripPrefix(type)) ? 'lab:admin' : 'lab:operate');

/** `localai.model.load` -> `model.load`. */
export const stripPrefix = (type: string): string => type.replace(/^localai\./, '');

export interface Gate {
  allowed: boolean;
  /** The plain-language reason a button is disabled. */
  reason: string | null;
  /** The doctor code behind the reason. */
  code: 'LAI-CLD-034' | 'LAI-CLD-035' | 'LAI-CLD-057' | null;
}

const OPEN: Gate = { allowed: true, reason: null, code: null };

export function offlineReason(mode: 'offline' | 'survival'): string {
  return `This computer is in ${mode === 'survival' ? 'Survival' : 'Offline'} mode. Only someone at it can switch it back.`;
}

/**
 * Whether `type` can be sent to this node right now. Order matches the server's: not enrolled (LAI-CLD-035), then
 * Offline or Survival mode (LAI-CLD-034); then the node's own allow list (LAI-CLD-057), which the server learns only
 * when the node refuses.
 */
export function gateFor(node: Pick<NodeDetail, 'nodeId' | 'phase' | 'mode' | 'acceptedTypes'>, type: string): Gate {
  if (node.phase !== 'enrolled') {
    return { allowed: false, code: 'LAI-CLD-035', reason: 'This node is not enrolled yet. Finish installing it on the computer first.' };
  }
  if (node.mode === 'offline' || node.mode === 'survival') {
    return { allowed: false, code: 'LAI-CLD-034', reason: offlineReason(node.mode) };
  }
  if (node.acceptedTypes !== null && !node.acceptedTypes.includes(stripPrefix(type))) {
    return { allowed: false, code: 'LAI-CLD-057', reason: `${node.nodeId} does not accept this command (disabled on the node)` };
  }
  return OPEN;
}

/** Numeric `major.minor.patch` comparison for "is the latest agent newer"; null when either is not a plain version. */
export function compareVersions(a: string | null, b: string | null): number | null {
  const parse = (value: string | null) => {
    const match = value ? /^(\d+)\.(\d+)\.(\d+)/.exec(value) : null;
    return match ? [Number(match[1]), Number(match[2]), Number(match[3])] : null;
  };
  const left = parse(a);
  const right = parse(b);
  if (!left || !right) return null;
  for (let index = 0; index < 3; index += 1) {
    if (left[index]! !== right[index]!) return left[index]! > right[index]! ? 1 : -1;
  }
  return 0;
}

export interface AgentUpdateState {
  /** The button shows at all (the setting is set). */
  visible: boolean;
  enabled: boolean;
  from: string | null;
  to: string | null;
  reason: string | null;
}

/**
 * The **Update agent…** button (issue #86): visible when `agent.latestVersion` is set; enabled only when it is greater
 * than the node's agent version, the node is online and enrolled, and the node allows `agent.update`.
 */
export function agentUpdateState(node: Pick<NodeDetail, 'nodeId' | 'phase' | 'mode' | 'health' | 'agentVersion' | 'agentLatestVersion' | 'acceptedTypes'>): AgentUpdateState {
  const to = node.agentLatestVersion;
  const from = node.agentVersion;
  if (!to) return { visible: false, enabled: false, from, to, reason: null };
  const newer = compareVersions(to, from) === 1;
  if (!newer) return { visible: true, enabled: false, from, to, reason: from ? `${node.nodeId} already runs ${from}` : 'The node has not reported its agent version yet' };
  const gate = gateFor(node, 'agent.update');
  if (!gate.allowed) return { visible: true, enabled: false, from, to, reason: gate.reason };
  if (node.mode !== 'online' || node.health === 'down') {
    return { visible: true, enabled: false, from, to, reason: `${node.nodeId} must be online to update` };
  }
  return { visible: true, enabled: true, from, to, reason: null };
}
