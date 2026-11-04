// The node-state refusals of the command API (docs/specs/cloud-app.md sections 3.9 and 5), as PURE functions of the
// node row and the parsed params, so the service and the tests read one rule. The cloud can never put a node into
// Survival or bring it back online; refusing here keeps that rule visible instead of letting a command expire.
import type { LocalaiNodePhase } from '@prisma/client';

import type { CommandType } from '../contracts/commands';
import { modelNotOnNode, nodeInOfflineMode, nodeNotEnrolled } from './command-errors';

/** The node fields the refusals read. */
export interface NodeForCommands {
  phase: LocalaiNodePhase;
  mode: string | null;
}

/** Throws 409 (retired/pending) then 422 (offline/survival per the last snapshot); returns when the node takes commands. */
export function assertNodeTakesCommands(node: NodeForCommands): void {
  if (node.phase !== 'enrolled') throw nodeNotEnrolled(node.phase);
  if (node.mode === 'offline' || node.mode === 'survival') throw nodeInOfflineMode(node.mode);
}

/** The types whose `model_id` must already be on the node: every model command except the download that fetches it. */
const MODEL_ON_NODE_TYPES: ReadonlySet<CommandType> = new Set<CommandType>([
  'localai.model.load',
  'localai.model.unload',
  'localai.model.set_default',
  'localai.model.delete',
]);

/** The model id a command must find on the node, or null when the type needs none. `params` must be parsed. */
export function modelRequiredOnNode(type: CommandType, params: unknown): string | null {
  if (!MODEL_ON_NODE_TYPES.has(type)) return null;
  const id = (params as { model_id?: unknown }).model_id;
  return typeof id === 'string' ? id : null;
}

/** Throws 422 unless the node's last snapshot lists the model as present on disk. */
export function assertModelOnNode(row: { present: boolean } | null): void {
  if (!row || !row.present) throw modelNotOnNode();
}
