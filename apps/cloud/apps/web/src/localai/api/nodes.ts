// The node routes of the detail page (docs/specs/cloud-app.md section 3.9). One thin function per route; the transport
// is the app's `api` client and errors are its `ApiError`.
import { api } from '../../api';
import type { NodeDetail, PatchNodeInput, ReplacedCredential, RetiredNode } from './types';

const path = (nodeId: string) => `/localai/nodes/${encodeURIComponent(nodeId)}`;

export const getNodeDetail = (nodeId: string): Promise<NodeDetail> => api.get<NodeDetail>(path(nodeId));

/** lab:admin only. The answer is audited (`localai.node.tailnet_revealed`); call it once per click, never on a timer. */
export const revealTailnetIp = async (nodeId: string): Promise<string | null> => (await api.get<NodeDetail>(`${path(nodeId)}?reveal=tailnet`)).tailnetIp ?? null;

export const patchNode = (nodeId: string, input: PatchNodeInput): Promise<NodeDetail> => api.patch<NodeDetail>(path(nodeId), input);

export const replaceNodeCredential = (nodeId: string): Promise<ReplacedCredential> => api.post<ReplacedCredential>(`${path(nodeId)}/credential`);

export const retireNode = (nodeId: string, confirm: string): Promise<RetiredNode> => api.post<RetiredNode>(`${path(nodeId)}/retire`, { confirm });
