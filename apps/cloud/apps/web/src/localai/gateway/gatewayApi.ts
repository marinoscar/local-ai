// The key routes and the two reads the Create dialog uses. One thin function per route; the transport is the app's `api`
// client, errors are its `ApiError` (the API's message is in `.message`).
import { api } from '../../api';
import type { CreatedKey, CreateKeyInput, GatewayKey, KeyDefaults, KeyFilters } from './types';

export const DEFAULT_KEY_DEFAULTS: KeyDefaults = { expiresInDays: 90, maxUserKeyDays: 365 };

export async function listKeys(filters: KeyFilters): Promise<GatewayKey[]> {
  const query = new URLSearchParams();
  if (filters.all) query.set('all', '1');
  if (filters.includeRevoked) query.set('includeRevoked', '1');
  const suffix = query.toString();
  return (await api.get<{ keys: GatewayKey[] }>(`/localai/gateway/keys${suffix ? `?${suffix}` : ''}`)).keys;
}

export const createKey = (input: CreateKeyInput): Promise<CreatedKey> => api.post<CreatedKey>('/localai/gateway/keys', input);

export const revokeKey = (id: string): Promise<GatewayKey> => api.delete<GatewayKey>(`/localai/gateway/keys/${encodeURIComponent(id)}`);

/** The default and the longest expiry of a user key (`gateway.defaults.user.expiresInDays`, `gateway.maxUserKeyDays`). */
export async function fetchKeyDefaults(): Promise<KeyDefaults> {
  const { settings } = await api.get<{ settings: Record<string, unknown> }>('/localai/settings');
  const user = settings['gateway.defaults.user'] as { expiresInDays?: unknown } | undefined;
  const max = settings['gateway.maxUserKeyDays'];
  const maxUserKeyDays = typeof max === 'number' && max >= 1 ? max : DEFAULT_KEY_DEFAULTS.maxUserKeyDays;
  const days = typeof user?.expiresInDays === 'number' && user.expiresInDays >= 1 ? user.expiresInDays : DEFAULT_KEY_DEFAULTS.expiresInDays;
  return { expiresInDays: Math.min(days, maxUserKeyDays), maxUserKeyDays };
}

/** Model ids to suggest in the Create dialog: those loaded on a node now. Suggestions only; a typed id is accepted. */
export async function fetchModelSuggestions(): Promise<string[]> {
  const { nodes } = await api.get<{ nodes: Array<{ loadedModels?: Array<{ id: string }> }> }>('/localai/nodes');
  return [...new Set(nodes.flatMap((node) => (node.loadedModels ?? []).map((model) => model.id)))].sort();
}
