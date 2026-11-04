// The command routes of the detail page (docs/specs/cloud-app.md section 3.9): the page never talks to a node, it queues
// a closed-type command and polls it.
import { api } from '../../api';
import type { CommandPage, CommandView } from './types';

const nodePath = (nodeId: string) => `/localai/nodes/${encodeURIComponent(nodeId)}`;

/** `type` without the `localai.` prefix is accepted by the API, and so is the prefixed name. */
export const sendCommand = (nodeId: string, type: string, params: Record<string, unknown> = {}): Promise<CommandView> =>
  api.post<CommandView>(`${nodePath(nodeId)}/commands`, { type, params });

export const getCommand = (id: string): Promise<CommandView> => api.get<CommandView>(`/localai/commands/${encodeURIComponent(id)}`);

/** Only a `queued` command can be cancelled (409 once a node claimed it). */
export const cancelCommand = (id: string): Promise<CommandView> => api.delete<CommandView>(`/localai/commands/${encodeURIComponent(id)}`);

/** The newest commands of the node (no `result`). */
export const listCommands = (nodeId: string, pageSize = 20): Promise<CommandPage> => api.get<CommandPage>(`${nodePath(nodeId)}/commands?pageSize=${pageSize}`);
