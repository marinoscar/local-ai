// Which model a request means and whether its key may use it (docs/specs/gateway.md section 4.1 "resolve model", then
// "scope check", and section 4.4 for the scope rule). Shared by `/v1/models` (this item) and the inference handlers
// (GW-06): `ModelResolver.resolve(requested)` then `assertKeyAllows(key, requested, resolved)`.
//
// RESOLVE. A request names a catalog id or an alias. It resolves when the id is in the APPROVED catalog and its policy is
// enabled (no policy row = enabled); an alias resolves to the id of the policy that lists it, if that policy is enabled
// and the id is in the catalog. An unresolved name is the caller's 404 LAI-GW-020 (unknown, or disabled: the same answer).
// A catalog id always wins over an alias spelled the same (policy writes keep them apart; this is the fallback).
//
// SCOPE. `allowedModels` empty = everything. Otherwise an entry that is a catalog id allows that id AND all its aliases;
// an entry that is an alias allows only that alias. Pure and total, so the listing and the authorisation cannot disagree.
import { Inject, Injectable } from '@nestjs/common';

import { LOCALAI_CLOCK, type LocalaiClock } from '../../commands/command-events';
import { GatewayError } from '../errors';
import type { ApprovedCatalog, CatalogModelHints } from './catalog-hints';
import { ApprovedCatalogReader } from './catalog-hints';
import { DEFAULT_MODEL_POLICY, MODEL_POLICY_SOURCE, type ModelPolicy, type ModelPolicySource } from './policy';

/** The catalog and the policies at one moment. */
export interface ModelDirectory {
  /** Null when no catalog is approved: nothing resolves and nothing is listed. */
  catalog: ApprovedCatalog | null;
  /** Stored policies by catalog id; a model without one has {@link DEFAULT_MODEL_POLICY}. */
  policies: ReadonlyMap<string, ModelPolicy>;
  /** alias -> catalog id, of enabled models that are in the catalog only. */
  aliasIndex: ReadonlyMap<string, string>;
}

/** A name resolved to a catalog model. */
export interface ResolvedModel {
  /** The catalog id. */
  id: string;
  /** The alias the request used; null when it used the id. */
  alias: string | null;
  policy: ModelPolicy;
  hints: CatalogModelHints;
}

export function policyOf(directory: Pick<ModelDirectory, 'policies'>, modelId: string): ModelPolicy {
  return directory.policies.get(modelId) ?? DEFAULT_MODEL_POLICY;
}

export function buildDirectory(catalog: ApprovedCatalog | null, policies: ReadonlyMap<string, ModelPolicy>): ModelDirectory {
  const aliasIndex = new Map<string, string>();
  if (catalog) {
    // Sorted, so a (forbidden) duplicated alias resolves the same way on every call.
    for (const id of [...policies.keys()].sort()) {
      const policy = policies.get(id)!;
      if (!policy.enabled || !catalog.models.has(id)) continue;
      for (const alias of policy.aliases) if (!catalog.models.has(alias) && !aliasIndex.has(alias)) aliasIndex.set(alias, id);
    }
  }
  return { catalog, policies, aliasIndex };
}

/** `requested` (a catalog id or an alias) to the catalog model, or null when it is unknown or its policy is disabled. */
export function resolveModel(directory: ModelDirectory, requested: string): ResolvedModel | null {
  const catalog = directory.catalog;
  if (!catalog) return null;
  const direct = catalog.models.get(requested);
  if (direct) {
    const policy = policyOf(directory, requested);
    return policy.enabled ? { id: requested, alias: null, policy, hints: direct } : null;
  }
  const id = directory.aliasIndex.get(requested);
  const hints = id === undefined ? undefined : catalog.models.get(id);
  if (id === undefined || !hints) return null;
  return { id, alias: requested, policy: policyOf(directory, id), hints };
}

/**
 * The scope rule on the two names of one entry: `aliasOf` is the catalog id for an alias entry, null for an id entry.
 * `allowedModels` empty allows everything.
 */
export function scopeAllows(allowedModels: readonly string[], name: string, aliasOf: string | null): boolean {
  if (allowedModels.length === 0) return true;
  return allowedModels.includes(name) || (aliasOf !== null && allowedModels.includes(aliasOf));
}

/** Whether `key` may use the model a request named; `requested` is the name as sent (id or alias). */
export function keyAllows(key: { allowedModels: readonly string[] }, requested: string, resolved: Pick<ResolvedModel, 'id'>): boolean {
  return scopeAllows(key.allowedModels, requested, requested === resolved.id ? null : resolved.id);
}

/** {@link keyAllows} that refuses with 403 `model_not_allowed` (LAI-GW-012). */
export function assertKeyAllows(key: { allowedModels: readonly string[] }, requested: string, resolved: Pick<ResolvedModel, 'id'>): void {
  if (!keyAllows(key, requested, resolved)) throw new GatewayError('LAI-GW-012');
}

/** How long the stored policies are trusted. GW-09's writes call {@link ModelDirectoryProvider.invalidate}. */
export const POLICY_TTL_MS = 5_000;

/** The directory, assembled from the cached catalog and the cached policies. */
@Injectable()
export class ModelDirectoryProvider {
  private cached: { policies: Map<string, ModelPolicy>; checkedAt: number } | null = null;
  private inflight: Promise<Map<string, ModelPolicy>> | null = null;

  constructor(
    private readonly catalogs: ApprovedCatalogReader,
    @Inject(MODEL_POLICY_SOURCE) private readonly source: ModelPolicySource,
    @Inject(LOCALAI_CLOCK) private readonly clock: LocalaiClock,
  ) {}

  async load(): Promise<ModelDirectory> {
    const [catalog, policies] = await Promise.all([this.catalogs.current(), this.policies()]);
    return buildDirectory(catalog, policies);
  }

  /** Drops what is cached: a catalog approval or a policy write shows on the next request. */
  invalidate(): void {
    this.cached = null;
    this.catalogs.invalidate();
  }

  private async policies(): Promise<Map<string, ModelPolicy>> {
    const now = this.clock.now().getTime();
    if (this.cached && now - this.cached.checkedAt < POLICY_TTL_MS) return this.cached.policies;
    this.inflight ??= this.source
      .all()
      .then((policies) => {
        this.cached = { policies, checkedAt: now };
        return policies;
      })
      .catch((error: unknown) => {
        // A failing read serves the last good policies; with none there is nothing safe to assume.
        if (this.cached) return this.cached.policies;
        throw error;
      })
      .finally(() => {
        this.inflight = null;
      });
    return this.inflight;
  }
}

/** `resolve(requested)` for the handlers. */
@Injectable()
export class ModelResolver {
  constructor(private readonly directory: ModelDirectoryProvider) {}

  async resolve(requested: string): Promise<ResolvedModel | null> {
    return resolveModel(await this.directory.load(), requested);
  }
}
