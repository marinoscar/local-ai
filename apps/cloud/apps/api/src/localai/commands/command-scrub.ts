// Scrubbing of text that came from a node (docs/specs/cloud-app.md sections 3.6, 3.7 and 6; the redaction rules of
// docs/architecture/security-model.md section 5 and the agent's `logx.ScrubString`). The agent scrubs before it
// sends; the cloud scrubs AGAIN before it stores, because a command result and a failure message are shown to
// people and kept for 180 days. Idempotent, and it returns its input unchanged when nothing matches.
//
// Rules, the same set as the agent's, in this order:
//   Bearer credentials, credential-prefixed tokens, 64-hex keys, tailnet addresses (the CGNAT range, second octet 64 to 127), the tailnet name.
const REDACTED = '[redacted]';

const BEARER = /Bearer\s+\S+/g;
const TOKEN = /\b(?:lat|nod|lai|pat|pg)_[A-Za-z0-9_-]{20,}/g;
const HEX64 = /\b[0-9a-f]{64}\b/g;
const CGNAT = /\b100\.(\d{1,3})\.\d{1,3}\.\d{1,3}\b/g;
const TAILNET_NAME = /tail[0-9a-f]{6}/g;

/** Whether the second octet of a `100.*` address is in the CGNAT range (64 to 127). */
function inCgnat(secondOctet: string): boolean {
  const n = Number(secondOctet);
  return n >= 64 && n <= 127;
}

/** Removes secrets and tailnet identifiers from one string. */
export function scrubText(input: string): string {
  if (input.length < 8) return input;
  let out = input;
  if (out.includes('Bearer')) out = out.replace(BEARER, `Bearer ${REDACTED}`);
  out = out.replace(TOKEN, REDACTED).replace(HEX64, REDACTED);
  if (out.includes('100.')) out = out.replace(CGNAT, (whole, second: string) => (inCgnat(second) ? REDACTED : whole));
  if (out.includes('tail')) out = out.replace(TAILNET_NAME, REDACTED);
  return out;
}

/** Scrubs every string inside a JSON value (arrays and objects recursively); other values pass through. */
export function scrubJson<T>(value: T): T {
  if (typeof value === 'string') return scrubText(value) as T;
  if (Array.isArray(value)) return value.map((item) => scrubJson(item)) as T;
  if (value !== null && typeof value === 'object') {
    return Object.fromEntries(Object.entries(value as Record<string, unknown>).map(([key, item]) => [key, scrubJson(item)])) as T;
  }
  return value;
}
