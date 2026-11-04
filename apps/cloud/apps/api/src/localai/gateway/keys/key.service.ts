// Gateway key management (docs/specs/gateway.md section 3.8): create, list, update, revoke.
//
// SECRETS. The raw token exists in `create` and in the value it returns, nowhere else: it is not stored (only its sha256
// hex and its 10-character display prefix are), not audited, not logged and not part of any error. List, update and revoke
// answer `KeyDto`, which has no token and no hash.
//
// VISIBILITY. A key is visible to its owner and to a gateway administrator. For anyone else it does not exist: update and
// revoke answer 404, not 403, so the id of someone else's key cannot be probed.
//
// ORDER. Every audit write and cache eviction runs AFTER the change committed; a failure there is logged (the error name
// only) and never undoes or fails the change.
import { BadRequestException, ConflictException, ForbiddenException, Inject, Injectable, Logger, NotFoundException, Optional } from '@nestjs/common';
import { AUDIT_SINK, type AuditSink } from '@marinoscar/platform-api/core';

import { LOCALAI_CLOCK, systemLocalaiClock, type LocalaiClock } from '../../commands/command-events';
import { GATEWAY_KEY_DEFAULTS, type GatewayKeyDefaultsReader } from '../../settings.service';
import { AUDIT_GATEWAY_KEY_CREATED, AUDIT_GATEWAY_KEY_REVOKED, AUDIT_GATEWAY_KEY_UPDATED, AUDIT_TARGET_GATEWAY_KEY } from '../gateway.audit';
import { GATEWAY_KEY_AUTH_CACHE, type KeyAuthCache } from '../key-auth-cache';
import { mintGatewayToken } from '../gateway.token';
import { isGatewayAdmin, resolveCreate, resolvePatch, type KeyActor } from './key-rules';
import { GATEWAY_KEY_STORE, type KeyRecord, type KeyStore } from './key.store';
import { GATEWAY_KNOWN_MODELS, type KnownModels } from './known-models';
import { toKeyDto, type CreateKeyBody, type CreatedKeyDto, type KeyDto, type ListKeysQuery, type PatchKeyBody } from './keys.schemas';

type AuditMeta = Record<string, string | number | boolean | null>;

@Injectable()
export class KeyService {
  private readonly logger = new Logger(KeyService.name);

  constructor(
    @Inject(GATEWAY_KEY_STORE) private readonly store: KeyStore,
    @Inject(GATEWAY_KEY_DEFAULTS) private readonly defaults: GatewayKeyDefaultsReader,
    @Inject(GATEWAY_KNOWN_MODELS) private readonly known: KnownModels,
    @Inject(AUDIT_SINK) private readonly audit: AuditSink,
    @Optional() @Inject(GATEWAY_KEY_AUTH_CACHE) private readonly cache: KeyAuthCache | null = null,
    @Optional() @Inject(LOCALAI_CLOCK) private readonly clock: LocalaiClock = systemLocalaiClock,
  ) {}

  /** The caller's own keys; with `all`, every key (gateway administrators only: 403 otherwise). */
  async list(actor: KeyActor, query: Pick<ListKeysQuery, 'all' | 'includeRevoked'>): Promise<KeyDto[]> {
    const admin = isGatewayAdmin(actor);
    if (query.all && !admin) throw new ForbiddenException('Only a gateway administrator can list every key');
    const rows = await this.store.list({ ...(query.all ? {} : { ownerUserId: actor.userId }), includeRevoked: query.includeRevoked });
    const now = this.clock.now();
    return rows.map((row) => toKeyDto(row, now, admin));
  }

  /** Creates a key and returns it with its raw token, the only time the token exists outside the caller's hands. */
  async create(actor: KeyActor, body: CreateKeyBody): Promise<CreatedKeyDto> {
    const now = this.clock.now();
    const resolved = resolveCreate(body, actor, await this.defaults.read(), now);
    if (resolved.ownerUserId !== actor.userId && !(await this.store.userExists(resolved.ownerUserId))) {
      throw new BadRequestException('The owner does not exist');
    }
    await this.requireKnownModels(resolved.allowedModels);

    const minted = mintGatewayToken();
    const row = await this.store.insert({
      userId: resolved.ownerUserId,
      name: body.name,
      kind: resolved.kind,
      tokenHash: minted.tokenHash,
      tokenPrefix: minted.tokenPrefix,
      allowedModels: resolved.allowedModels,
      rpmLimit: resolved.rpm,
      concurrentLimit: resolved.concurrent,
      dailyTokenBudget: resolved.dailyTokens === null ? null : BigInt(resolved.dailyTokens),
      expiresAt: resolved.expiresAt,
      createdById: actor.userId,
    });
    await this.record(AUDIT_GATEWAY_KEY_CREATED, actor, row, {
      prefix: row.tokenPrefix,
      name: row.name,
      kind: row.kind,
      ownerUserId: row.userId,
      rpm: row.rpmLimit,
      concurrent: row.concurrentLimit,
      dailyTokens: row.dailyTokenBudget === null ? null : Number(row.dailyTokenBudget),
      expiresAt: row.expiresAt ? row.expiresAt.toISOString() : null,
      allowedModels: modelsMeta(row.allowedModels),
    });
    return { key: toKeyDto(row, now, isGatewayAdmin(actor)), token: minted.token };
  }

  /** Changes a key's name, models or limits. The owner may rename and narrow; a gateway administrator may do the rest. */
  async update(actor: KeyActor, id: string, body: PatchKeyBody): Promise<KeyDto> {
    const existing = await this.requireVisible(actor, id);
    if (existing.revokedAt !== null) throw new ConflictException('The key is revoked');
    const resolved = resolvePatch(existing, body, actor);
    await this.requireKnownModels(resolved.addedModels);

    const row = await this.store.update(id, resolved.changes);
    if (row === null) throw new ConflictException('The key is revoked');
    await this.evict(row.tokenHash);
    await this.record(AUDIT_GATEWAY_KEY_UPDATED, actor, row, {
      prefix: row.tokenPrefix,
      name: row.name,
      changed: resolved.changed.join(','),
      rpm: row.rpmLimit,
      concurrent: row.concurrentLimit,
      dailyTokens: row.dailyTokenBudget === null ? null : Number(row.dailyTokenBudget),
      allowedModels: modelsMeta(row.allowedModels),
    });
    return toKeyDto(row, this.clock.now(), isGatewayAdmin(actor));
  }

  /** Revokes a key: effective on the next request. A second revoke is a no-op that answers the same key. */
  async revoke(actor: KeyActor, id: string): Promise<KeyDto> {
    const existing = await this.requireVisible(actor, id);
    const revoked = await this.store.revoke(id, this.clock.now());
    // Evict even when someone else revoked it first: the entry must not outlive the revocation.
    await this.evict(existing.tokenHash);
    const row = (await this.store.findById(id)) ?? existing;
    if (revoked) {
      await this.record(AUDIT_GATEWAY_KEY_REVOKED, actor, row, { prefix: row.tokenPrefix, name: row.name, kind: row.kind, ownerUserId: row.userId });
    }
    return toKeyDto(row, this.clock.now(), isGatewayAdmin(actor));
  }

  private async requireVisible(actor: KeyActor, id: string): Promise<KeyRecord> {
    const row = await this.store.findById(id);
    if (row === null || (row.userId !== actor.userId && !isGatewayAdmin(actor))) throw new NotFoundException('No such key');
    return row;
  }

  private async requireKnownModels(names: readonly string[]): Promise<void> {
    if (names.length === 0) return;
    const known = await this.known.names();
    const unknown = names.filter((name) => !known.has(name));
    if (unknown.length > 0) {
      throw new BadRequestException({
        message: `Unknown model: ${unknown.join(', ')}`,
        details: { issues: unknown.map((name) => ({ path: 'allowedModels', message: `${name} is not a model id or alias of this lab` })) },
      });
    }
  }

  private async evict(tokenHash: string): Promise<void> {
    if (this.cache === null) return;
    try {
      await this.cache.evict(tokenHash);
    } catch (error) {
      this.logger.warn(`evicting a key from the auth cache failed (${error instanceof Error ? error.name : 'error'}); it expires on its own within 10 s`);
    }
  }

  private async record(action: string, actor: KeyActor, row: KeyRecord, meta: AuditMeta): Promise<void> {
    try {
      await this.audit.record({ action, actorUserId: actor.userId, targetType: AUDIT_TARGET_GATEWAY_KEY, targetId: row.id, meta });
    } catch (error) {
      this.logger.error(`recording ${action} for key ${row.tokenPrefix} failed (${error instanceof Error ? error.name : 'error'})`);
    }
  }
}

/** The audit text of an allowed-models list: `all`, or the names joined (bounded). */
function modelsMeta(names: readonly string[]): string {
  if (names.length === 0) return 'all';
  const joined = names.join(',');
  return joined.length <= 300 ? joined : `${joined.slice(0, 297)}...`;
}
