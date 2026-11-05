// The two model routes. One thin function per route; the transport is the app's `api` client, errors are its `ApiError`
// (the API's message, for example "The alias lab-coder already belongs to ...", is in `.message`).
import { api } from '../../api';
import type { ModelRow, PolicyInput } from './modelsTypes';

export async function listModels(): Promise<ModelRow[]> {
  return (await api.get<{ models: ModelRow[] }>('/localai/gateway/models')).models;
}

export const saveModelPolicy = (modelId: string, input: PolicyInput): Promise<ModelRow> =>
  api.put<ModelRow>(`/localai/gateway/models/${encodeURIComponent(modelId)}`, input);
