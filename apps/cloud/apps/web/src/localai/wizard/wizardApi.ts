// The wizard's transport, replaceable in tests. Every call goes through the app's one HTTP client (`/api`, bearer token).
//
// The create call asks the browser not to cache the response (`cache: 'no-store'`): it carries the node credential, which
// exists nowhere else. The server says the same with `Cache-Control: no-store`.
import { api } from '../../api';
import type { Checklist, ChecklistAction, ChecklistStepId, CreateNodeInput, CreateNodeResult, NodeStatus, VpsStatus } from './wizardTypes';

export interface WizardClient {
  nextNodeId(): Promise<string | null>;
  createNode(input: CreateNodeInput): Promise<CreateNodeResult>;
  nodeStatus(nodeId: string): Promise<NodeStatus>;
  checklist(nodeId: string): Promise<Checklist>;
  act(nodeId: string, step: ChecklistStepId, action: ChecklistAction): Promise<Checklist>;
  /** Automatic publishing (section 4.10): what blocks it, and the journal of the last run. */
  vpsStatus(nodeId: string): Promise<VpsStatus>;
  vpsPublish(nodeId: string): Promise<{ jobId: string }>;
  vpsUnpublish(nodeId: string): Promise<{ jobId: string }>;
}

const NO_STORE = { cache: 'no-store' } as const;

export const defaultWizardClient: WizardClient = {
  nextNodeId: async () => (await api.get<{ nodeId: string | null }>('/localai/nodes/next-id', NO_STORE)).nodeId,
  createNode: (input) => api.post<CreateNodeResult>('/localai/nodes', input, NO_STORE),
  nodeStatus: (nodeId) => api.get<NodeStatus>(`/localai/nodes/${encodeURIComponent(nodeId)}`, NO_STORE),
  checklist: (nodeId) => api.get<Checklist>(`/localai/nodes/${encodeURIComponent(nodeId)}/checklist`, NO_STORE),
  act: (nodeId, step, action) => api.post<Checklist>(`/localai/nodes/${encodeURIComponent(nodeId)}/checklist/${step}`, { action }, NO_STORE),
  vpsStatus: (nodeId) => api.get<VpsStatus>(`/localai/nodes/${encodeURIComponent(nodeId)}/publish`, NO_STORE),
  vpsPublish: (nodeId) => api.post<{ jobId: string }>(`/localai/nodes/${encodeURIComponent(nodeId)}/publish`, {}, NO_STORE),
  vpsUnpublish: (nodeId) => api.post<{ jobId: string }>(`/localai/nodes/${encodeURIComponent(nodeId)}/unpublish`, {}, NO_STORE),
};

/** The reason code the API put in `details.reason` of a refusal, if any (`node_id_used`, `automatic_step`, ...). */
export function reasonOf(error: unknown): string | null {
  const details = (error as { details?: unknown } | null)?.details;
  const reason = typeof details === 'object' && details !== null ? (details as { reason?: unknown }).reason : undefined;
  return typeof reason === 'string' ? reason : null;
}

/** A sentence for an operator: the API's own message, which is plain language and carries no secret. */
export function messageOf(error: unknown, fallback: string): string {
  return error instanceof Error && error.message ? error.message : fallback;
}
