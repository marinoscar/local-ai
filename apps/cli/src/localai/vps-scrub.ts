// Scrubbing of every string the stack-agent returns, logs or puts in an error (docs/specs/cloud-app.md sections 4.10
// and 6). The executor never builds a detail from a command's output or a remote error message; it uses fixed
// sentences. This is the second line of defence: whatever reaches a result passes through here, and the tests assert
// that nothing the tripwire looks for survives it.
const REDACTED = '[redacted]';

const PATTERNS: ReadonlyArray<RegExp> = [
  // An AWS access key id (long-term or session).
  /\b(?:AKIA|ASIA|AIDA|AROA)[A-Z0-9]{12,}\b/g,
  // A 40-character base64-ish secret key, when it stands alone.
  /(?<![A-Za-z0-9/+=])[A-Za-z0-9/+]{40}(?![A-Za-z0-9/+=])/g,
  // Bearer credentials and the lab's token prefixes.
  /Bearer\s+\S+/g,
  /\b(?:lat|nod|lai|pat)_[A-Za-z0-9_-]{20,}/g,
  // 64 hex characters.
  /\b[0-9a-fA-F]{64}\b/g,
  // Every IPv4 address (the tailnet's, the VPS's, a resolver's): a journal never needs one.
  /(?<![0-9A-Za-z.])(?:\d{1,3}\.){3}\d{1,3}(?![0-9A-Za-z]|\.\d)/g,
  // A tailnet IPv6 address and a MagicDNS name.
  /\bfd7a:115c:a1e0(?::[0-9a-f]{1,4}){1,5}\b/gi,
  /\b[a-z0-9-]+\.tail[0-9a-f]{4,}\.ts\.net\b/gi,
  /\.ts\.net\b/gi,
];

/** Removes every secret and address pattern from one string and caps its length. */
export function scrubDetail(input: string, maxLength = 500): string {
  let out = input;
  for (const pattern of PATTERNS) out = out.replace(pattern, REDACTED);
  out = out.replace(/[\r\n\t]+/g, ' ').trim();
  return out.length > maxLength ? `${out.slice(0, maxLength - 1)}…` : out;
}
