// The seam between key management (GW-02) and the `/v1` key authentication (GW-03): the cache of positive
// authentication results. GW-03's cache implements `KeyAuthCache` and binds it to `GATEWAY_KEY_AUTH_CACHE` from a global
// module (or a module `KeysModule` imports); key management calls `evict` whenever a key is revoked or its scope or limits
// change, so the change is effective on the next request instead of after the cache's 10 s lifetime
// (docs/specs/gateway.md section 3.8). The cache is keyed by the token hash, never by the token.
//
// Until GW-03 binds it there is no cache and nothing to evict: `KeyService` injects the token as @Optional.

/** Injection token of the cache. */
export const GATEWAY_KEY_AUTH_CACHE = Symbol('GATEWAY_KEY_AUTH_CACHE');

export interface KeyAuthCache {
  /** Forgets the cached authentication of the key whose token hash is `tokenHash` (sha256 hex). Must not throw for an unknown hash. */
  evict(tokenHash: string): void | Promise<void>;
}
