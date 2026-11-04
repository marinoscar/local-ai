// Gateway key token handling for the REQUEST path (docs/specs/gateway.md sections 2 and 4.1): the shape check that
// runs before any database query, the sha256 hex that is looked up, and the constant-time comparison. Key CREATION
// (generating the token, storing the hash) is the key-management item; this file only verifies what a client sends.
import { timingSafeEqual } from 'node:crypto';

import { GATEWAY_TOKEN_PATTERN, hashGatewayToken } from './gateway.token';

// Creation and hashing are `gateway.token.ts` (key management mints with the same functions this file verifies with).
export { hashGatewayToken };

/** `lai_` + 43 base64url characters (32 random bytes). Anything else is refused without a DB query. */
export const GATEWAY_KEY_REGEX = GATEWAY_TOKEN_PATTERN;

export function isWellFormedGatewayKey(token: string): boolean {
  return GATEWAY_KEY_REGEX.test(token);
}

/** Constant-time equality of two sha256 hex strings (false for different lengths, without comparing). */
export function hashesEqual(a: string, b: string): boolean {
  const left = Buffer.from(a, 'utf8');
  const right = Buffer.from(b, 'utf8');
  return left.length === right.length && timingSafeEqual(left, right);
}

/** The token of an `Authorization: Bearer <token>` header, or null when the header is absent or not a bearer one. */
export function bearerToken(header: string | string[] | undefined): string | null {
  const value = Array.isArray(header) ? header[0] : header;
  if (typeof value !== 'string') return null;
  const match = /^Bearer[ \t]+(\S+)[ \t]*$/i.exec(value);
  return match ? match[1]! : null;
}
