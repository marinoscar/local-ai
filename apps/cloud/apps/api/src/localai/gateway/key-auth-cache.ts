// The seam between key management (GW-02) and the `/v1` key authentication (GW-03): the cache of positive
// authentication results. `GatewayAuthenticator` (GW-03, `auth.ts`) implements `KeyAuthCache` with its `evict`, and
// `KeysModule` binds it to `GATEWAY_KEY_AUTH_CACHE`. Key management calls `evict` whenever a key is revoked or its scope or
// limits change, so the change is effective on the next request instead of after the cache's 10 s lifetime
// (docs/specs/gateway.md section 3.8). The cache is keyed by the token hash, never by the token.
//
// `KeyService` still injects the token as @Optional, so a unit test (or a deployment without the gateway core) just has no
// cache to evict.

/** Injection token of the cache. */
export const GATEWAY_KEY_AUTH_CACHE = Symbol('GATEWAY_KEY_AUTH_CACHE');

export interface KeyAuthCache {
  /** Forgets the cached authentication of the key whose token hash is `tokenHash` (sha256 hex). Must not throw for an unknown hash. */
  evict(tokenHash: string): void | Promise<void>;
}
