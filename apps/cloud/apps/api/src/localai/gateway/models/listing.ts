// `GET /v1/models` (docs/specs/gateway.md section 3.4 and 4.4): the ids a key can request RIGHT NOW.
//
// The union, over the enabled models of the approved catalog, of the models with at least one loaded routable candidate
// (`state: loaded`) and the models with on-demand load on and at least one loadable routable candidate
// (`state: loadable`, with `load_wait_s`). "Routable candidate" is the routing table's healthy filter (not suspect, not
// `keyInvalid`), the same as step 1 of the selection, so a listed id is one the selection can serve. Each alias of an
// enabled model is its own entry. The key's scope filters the entries (`scopeAllows`). Loaded first, then by id.
//
// A node is named by its id only: no host, no address, no tailnet name ever reaches this module's output.
import { Injectable } from '@nestjs/common';

import { RoutingTable } from '../routing/routing-table';
import type { SelectedCandidate } from '../routing/routing.types';
import { ModelDirectoryProvider, policyOf, scopeAllows, type ModelDirectory } from './resolve';

export type ModelState = 'loaded' | 'loadable';

/** The `x-localai` object of one entry. */
export interface ModelEntryExtension {
  state: ModelState;
  /** Node ids that serve it (loaded) or could load it (loadable). */
  nodes: string[];
  display_name: string;
  roles: string[];
  input_modalities: string[];
  context_length: number | null;
  max_output_tokens: number | null;
  reasoning: boolean;
  alias_of: string | null;
  /** Only for `loadable`: the seconds a request waits for the load. */
  load_wait_s?: number;
}

export interface ModelEntry {
  id: string;
  object: 'model';
  created: number;
  owned_by: 'localai';
  'x-localai': ModelEntryExtension;
}

export interface ModelList {
  object: 'list';
  data: ModelEntry[];
}

/** The candidates of a model a request could use now (the routing table's `healthyCandidates`). */
export type HealthyCandidates = (modelId: string) => readonly SelectedCandidate[];

const MODALITY_ORDER = ['text', 'image', 'audio', 'video'];

/** `lab-coder` -> `Lab coder`: the display name of an alias, which has no catalog entry of its own. */
export function aliasDisplayName(alias: string): string {
  const words = alias.replace(/[-_]+/g, ' ').trim();
  return words === '' ? alias : words.charAt(0).toUpperCase() + words.slice(1);
}

const compareText = (a: string, b: string): number => (a < b ? -1 : a > b ? 1 : 0);

/** The modalities the nodes report, in the canonical order, unknown ones last; `text` when none reported any. */
function modalitiesOf(candidates: readonly SelectedCandidate[]): string[] {
  const seen = new Set<string>();
  for (const candidate of candidates) for (const modality of candidate.inputModalities) seen.add(modality);
  if (seen.size === 0) return ['text'];
  const rank = (modality: string): number => {
    const index = MODALITY_ORDER.indexOf(modality);
    return index === -1 ? MODALITY_ORDER.length : index;
  };
  return [...seen].sort((a, b) => rank(a) - rank(b) || compareText(a, b));
}

/** Every entry for `directory` and the live candidates, scope-filtered and sorted. Pure. */
export function buildModelList(directory: ModelDirectory, candidatesOf: HealthyCandidates, allowedModels: readonly string[]): ModelEntry[] {
  const catalog = directory.catalog;
  if (!catalog) return [];
  const entries: ModelEntry[] = [];

  for (const [modelId, hints] of catalog.models) {
    const policy = policyOf(directory, modelId);
    if (!policy.enabled) continue;
    const candidates = candidatesOf(modelId);
    const loaded = candidates.filter((candidate) => candidate.status === 'loaded');
    const loadable = candidates.filter((candidate) => candidate.status === 'loadable');
    let state: ModelState;
    let serving: readonly SelectedCandidate[];
    if (loaded.length > 0) {
      state = 'loaded';
      serving = loaded;
    } else if (policy.onDemandLoad && loadable.length > 0) {
      state = 'loadable';
      serving = loadable;
    } else {
      continue;
    }

    const contexts = serving.map((candidate) => candidate.ctx).filter((ctx): ctx is number => ctx !== null);
    const base: Omit<ModelEntryExtension, 'display_name' | 'alias_of'> = {
      state,
      nodes: [...new Set(serving.map((candidate) => candidate.nodeId))].sort(compareText),
      roles: [...hints.roles],
      input_modalities: modalitiesOf(serving),
      context_length: contexts.length > 0 ? Math.max(...contexts) : hints.ctx,
      max_output_tokens: hints.maxOutputTokens,
      reasoning: hints.reasoning,
      ...(state === 'loadable' ? { load_wait_s: policy.loadWaitS } : {}),
    };
    const entry = (id: string, displayName: string, aliasOf: string | null): ModelEntry => ({
      id,
      object: 'model',
      created: catalog.createdSeconds,
      owned_by: 'localai',
      'x-localai': { ...base, nodes: [...base.nodes], roles: [...base.roles], input_modalities: [...base.input_modalities], display_name: displayName, alias_of: aliasOf },
    });

    if (scopeAllows(allowedModels, modelId, null)) entries.push(entry(modelId, hints.displayName, null));
    for (const alias of policy.aliases) {
      // The same rule as the index: an alias that is also a catalog id, or that another model owns, is not an alias here.
      if (directory.aliasIndex.get(alias) !== modelId) continue;
      if (scopeAllows(allowedModels, alias, modelId)) entries.push(entry(alias, aliasDisplayName(alias), modelId));
    }
  }

  return entries.sort((a, b) => {
    const stateRank = (entry: ModelEntry): number => (entry['x-localai'].state === 'loaded' ? 0 : 1);
    return stateRank(a) - stateRank(b) || compareText(a.id, b.id);
  });
}

@Injectable()
export class ModelListing {
  constructor(
    private readonly directory: ModelDirectoryProvider,
    private readonly table: RoutingTable,
  ) {}

  /** The entries `key` may see now. An empty list when no catalog is approved. */
  async build(key: { allowedModels: readonly string[] }): Promise<ModelEntry[]> {
    const directory = await this.directory.load();
    return buildModelList(directory, (modelId) => this.table.healthyCandidates(modelId), key.allowedModels);
  }

  /** The `{ object: 'list', data }` body. */
  async list(key: { allowedModels: readonly string[] }): Promise<ModelList> {
    return { object: 'list', data: await this.build(key) };
  }

  /** One entry by id or alias, or null when it is not listed for `key`. */
  async find(key: { allowedModels: readonly string[] }, id: string): Promise<ModelEntry | null> {
    return (await this.build(key)).find((entry) => entry.id === id) ?? null;
  }
}
