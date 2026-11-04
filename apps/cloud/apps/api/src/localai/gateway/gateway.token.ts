// The gateway key token: how it is made, shown, hashed and recognised (docs/specs/gateway.md sections 2 and 6).
//
// SHARED. `KeyService` (GW-02) mints and hashes with these functions; the `/v1` key authentication (GW-03) must parse and
// hash with the SAME ones, so a token the cloud issued is a token the gateway finds. Keep this file free of Nest, Prisma
// and I/O so either side can import it.
//
// The format is permanent (it is printed in client configuration files and matched by secret scanners):
//   token       `lai_` + base64url(32 random bytes), no padding: 4 + 43 = 47 characters
//   tokenHash   lowercase sha256 hex (64 characters) of the whole token including `lai_`; the only form ever stored
//   tokenPrefix `lai_` + the first 6 characters of the random part: display only, never enough to find a key
import { createHash, randomBytes } from 'node:crypto';

/** The fixed start of every gateway token. */
export const GATEWAY_TOKEN_SCHEME = 'lai_';

/** How many random bytes a token carries. */
export const GATEWAY_TOKEN_RANDOM_BYTES = 32;

/** The length of the base64url text of {@link GATEWAY_TOKEN_RANDOM_BYTES} bytes without padding. */
export const GATEWAY_TOKEN_BODY_LENGTH = 43;

/** The length of a whole token (`lai_` plus the body). */
export const GATEWAY_TOKEN_LENGTH = GATEWAY_TOKEN_SCHEME.length + GATEWAY_TOKEN_BODY_LENGTH;

/** How many characters of the body the display prefix keeps. */
export const GATEWAY_TOKEN_PREFIX_BODY_LENGTH = 6;

/** A well-formed gateway token. Anything else is rejected before the database is asked. */
export const GATEWAY_TOKEN_PATTERN = /^lai_[A-Za-z0-9_-]{43}$/;

/** A stored hash: 64 lowercase hex characters. */
export const GATEWAY_TOKEN_HASH_PATTERN = /^[0-9a-f]{64}$/;

/** Where the random bytes come from. Injectable so a test can pin them; production uses `crypto.randomBytes`. */
export type RandomBytes = (size: number) => Buffer;

/** Mints a new token. The only place the raw secret is created. */
export function generateGatewayToken(random: RandomBytes = randomBytes): string {
  return `${GATEWAY_TOKEN_SCHEME}${random(GATEWAY_TOKEN_RANDOM_BYTES).toString('base64url')}`;
}

/** The sha256 hex digest of a whole token: what `localai_gateway_keys.token_hash` holds and what a lookup compares. */
export function hashGatewayToken(token: string): string {
  return createHash('sha256').update(token, 'utf8').digest('hex');
}

/** The display prefix of a token: `lai_` plus the first 6 characters of its body (`lai_Ab3dEf`). */
export function gatewayTokenPrefix(token: string): string {
  return token.slice(0, GATEWAY_TOKEN_SCHEME.length + GATEWAY_TOKEN_PREFIX_BODY_LENGTH);
}

/** True for a string shaped like a gateway token. Says nothing about whether the key exists. */
export function isWellFormedGatewayToken(value: unknown): value is string {
  return typeof value === 'string' && GATEWAY_TOKEN_PATTERN.test(value);
}

/** A freshly minted token with the two values that are stored beside it. */
export interface MintedGatewayToken {
  /** The raw secret. Returned to the creator once and never stored or logged. */
  readonly token: string;
  readonly tokenHash: string;
  readonly tokenPrefix: string;
}

/** Mints a token and derives its hash and display prefix in one step. */
export function mintGatewayToken(random: RandomBytes = randomBytes): MintedGatewayToken {
  const token = generateGatewayToken(random);
  return { token, tokenHash: hashGatewayToken(token), tokenPrefix: gatewayTokenPrefix(token) };
}
