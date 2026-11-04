// Gateway key authentication (docs/specs/gateway.md section 4.1): `Authorization: Bearer lai_...` -> shape check (no DB
// query for a malformed key) -> sha256 -> key row (10 s positive cache, negative results never cached) -> revoked or
// expired? -> owner active and holding `gateway:use` (60 s cache) -> `lastUsedAt` fire-and-forget at most once a minute
// per key. Failed attempts feed the per-IP limiter. The class takes ports (a key store, an owner-access reader) so the
// table of refusals is unit tested without a database; `auth.prisma.ts` holds the Prisma adapters.
import { GatewayError } from './errors';
import { TtlCache, KEY_CACHE_TTL_MS, OWNER_CACHE_TTL_MS, type Clock } from './auth-cache';
import { FailedAuthLimiter } from './failed-auth-limiter';
import { bearerToken, hashGatewayToken, hashesEqual, isWellFormedGatewayKey } from './token-verify';

/** The permission a key's owner must hold (`localai.permissions.ts`). */
export const GATEWAY_USE_PERMISSION = 'gateway:use';
/** `lastUsedAt` is written at most this often per key. */
export const LAST_USED_INTERVAL_MS = 60_000;

/** The columns of `localai_gateway_keys` the request path reads. */
export interface GatewayKeyRecord {
  id: string;
  userId: string;
  name: string;
  kind: 'user' | 'service';
  tokenHash: string;
  tokenPrefix: string;
  allowedModels: string[];
  rpmLimit: number;
  concurrentLimit: number;
  /** null = unlimited (service keys only). */
  dailyTokenBudget: bigint | null;
  /** null only for a service key. */
  expiresAt: Date | null;
  revokedAt: Date | null;
}

export interface GatewayKeyStore {
  findByTokenHash(tokenHash: string): Promise<GatewayKeyRecord | null>;
  touchLastUsed(keyId: string, at: Date): Promise<void>;
}

export interface OwnerAccess {
  /** `User.isActive`. */
  active: boolean;
  /** Effective permission strings (the platform's `PrincipalFactory`). */
  permissions: readonly string[];
}

export interface OwnerAccessReader {
  /** null when the user no longer exists. */
  read(userId: string): Promise<OwnerAccess | null>;
}

/** What `authenticate()` hands to the rest of the pipeline. */
export interface GatewayPrincipal {
  key: GatewayKeyRecord;
  userId: string;
}

export interface AuthenticateInput {
  /** The raw `Authorization` header. */
  authorization: string | string[] | undefined;
  /** The client address (`clientIp`). */
  ip: string;
}

export interface GatewayAuthenticatorOptions {
  store: GatewayKeyStore;
  owners: OwnerAccessReader;
  limiter?: FailedAuthLimiter;
  now?: Clock;
  /** Called when a fire-and-forget `lastUsedAt` write fails (the request is never affected). */
  onTouchError?: (error: unknown) => void;
}

export class GatewayAuthenticator {
  private readonly store: GatewayKeyStore;
  private readonly owners: OwnerAccessReader;
  private readonly limiter: FailedAuthLimiter;
  private readonly now: Clock;
  private readonly keyCache: TtlCache<GatewayKeyRecord>;
  private readonly ownerCache: TtlCache<OwnerAccess | null>;
  private readonly lastTouch = new Map<string, number>();
  private readonly onTouchError: (error: unknown) => void;

  constructor(options: GatewayAuthenticatorOptions) {
    this.store = options.store;
    this.owners = options.owners;
    this.now = options.now ?? Date.now;
    this.limiter = options.limiter ?? new FailedAuthLimiter(this.now);
    this.keyCache = new TtlCache(KEY_CACHE_TTL_MS, this.now);
    this.ownerCache = new TtlCache(OWNER_CACHE_TTL_MS, this.now);
    this.onTouchError = options.onTouchError ?? (() => undefined);
  }

  /**
   * Resolves the caller or throws a `GatewayError`: 429 LAI-GW-011 (blocked address), 401 LAI-GW-010 (no, malformed,
   * unknown, revoked or expired key; each counts as a failure for the address), 403 LAI-GW-013 (owner deactivated or
   * without `gateway:use`; not a failure of the address).
   */
  async authenticate(input: AuthenticateInput): Promise<GatewayPrincipal> {
    const blockedFor = this.limiter.blockedForSeconds(input.ip);
    if (blockedFor !== null) throw new GatewayError('LAI-GW-011', { retryAfterS: blockedFor });

    const token = bearerToken(input.authorization);
    if (token === null || !isWellFormedGatewayKey(token)) return this.refuse(input.ip);

    const tokenHash = hashGatewayToken(token);
    let key = this.keyCache.get(tokenHash);
    if (!key) {
      const found = await this.store.findByTokenHash(tokenHash);
      if (!found || !hashesEqual(found.tokenHash, tokenHash)) return this.refuse(input.ip);
      key = found;
      if (key.revokedAt !== null) return this.refuse(input.ip);
    }
    if (key.expiresAt !== null && key.expiresAt.getTime() <= this.now()) {
      this.keyCache.evict(tokenHash);
      return this.refuse(input.ip);
    }
    // Only a key that passed every check is cached, so a revoked or expired row is looked up again next time.
    this.keyCache.set(tokenHash, key);

    const owner = await this.ownerAccess(key.userId);
    if (!owner || !owner.active || !owner.permissions.includes(GATEWAY_USE_PERMISSION)) throw new GatewayError('LAI-GW-013');

    this.touchLastUsed(key.id);
    return { key, userId: key.userId };
  }

  /** Forgets a key's cached lookup; the revoke path calls it so the next request is refused at once. */
  evict(tokenHash: string): void {
    this.keyCache.evict(tokenHash);
  }

  /** Forgets an owner's cached permissions (a role change that must apply before the 60 s expiry). */
  evictOwner(userId: string): void {
    this.ownerCache.evict(userId);
  }

  private refuse(ip: string): never {
    this.limiter.recordFailure(ip);
    throw new GatewayError('LAI-GW-010');
  }

  private async ownerAccess(userId: string): Promise<OwnerAccess | null> {
    const cached = this.ownerCache.get(userId);
    if (cached !== undefined) return cached;
    const access = await this.owners.read(userId);
    this.ownerCache.set(userId, access);
    return access;
  }

  private touchLastUsed(keyId: string): void {
    const now = this.now();
    const last = this.lastTouch.get(keyId);
    if (last !== undefined && now - last < LAST_USED_INTERVAL_MS) return;
    if (this.lastTouch.size > 10_000) this.lastTouch.clear();
    this.lastTouch.set(keyId, now);
    // Fire and forget: a failed write never fails or slows the request.
    void this.store.touchLastUsed(keyId, new Date(now)).catch(this.onTouchError);
  }
}

/** The client address: the inner nginx's `X-Real-IP`, else the socket address. */
export function clientIp(headers: Record<string, string | string[] | undefined>, socketIp: string | undefined): string {
  const header = headers['x-real-ip'];
  const value = (Array.isArray(header) ? header[0] : header)?.trim();
  if (value && value.length <= 64 && /^[0-9A-Fa-f:.]+$/.test(value)) return value;
  return socketIp && socketIp.length > 0 ? socketIp : 'unknown';
}
