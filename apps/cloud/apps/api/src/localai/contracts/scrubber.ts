// The secret scrubber for node-supplied JSON (docs/specs/cloud-app.md section 3.6 "Rules" and 4.3; patterns of
// docs/architecture/security-model.md section 5).
//
// A snapshot is schema-closed and the agent never puts a key, a token, a tailnet IP, a LAN IP or a path under
// /home in it. The cloud does not trust that: every string VALUE (and every object KEY, because `drill` check
// details are a free-form record) is tested against the redaction patterns, and a single hit refuses the whole
// snapshot. The caller logs only `path`, never the value.
//
// The walk is iterative with a depth cap, so a hostile body that is nested 100000 deep (it fits in 256 KiB) cannot
// overflow the stack.

/** What the scrubber looks for. The `rule` names are stable: they go into logs and tests, never the matched value. */
export type ScrubRule = 'node_token' | 'hex_key' | 'tailnet_ip' | 'private_ip' | 'home_path';

export interface ScrubFinding {
  /** JSON path of the offending string, e.g. `ark.roots[0].ready.items[3].reason`. Never contains the value. */
  path: string;
  rule: ScrubRule;
}

/** A body deeper than this is not a snapshot (the real one is about 8 deep). */
export const SCRUB_MAX_DEPTH = 24;
/** A hard stop for the walk (a 256 KiB body cannot legitimately hold more nodes than this). */
export const SCRUB_MAX_NODES = 200_000;

const OCTET = '(?:25[0-5]|2[0-4][0-9]|1[0-9]{2}|[1-9]?[0-9])';
// An IPv4 address that is not part of a longer dotted number or word (a version "10.1.2.3.4" or "v110.1.2.3" is not one).
const BEFORE = '(?<![0-9A-Za-z.])';
const AFTER = '(?![0-9A-Za-z]|\\.[0-9])';

const RULES: ReadonlyArray<readonly [ScrubRule, RegExp]> = [
  // security-model.md section 5: `lat_`, `nod_`, `pat_`, `lai_` tokens (the word boundary keeps `localai_*` names fine).
  ['node_token', /\b(?:lat|nod|lai|pat)_[A-Za-z0-9_-]{20,}/],
  // 64 hex characters: a sha256 or a 256-bit key.
  ['hex_key', /\b[0-9a-fA-F]{64}\b/],
  // The tailnet (CGNAT) range, a /10 block: first octet 100, second octet 64 to 127.
  ['tailnet_ip', new RegExp(`${BEFORE}100\\.(?:6[4-9]|[7-9][0-9]|1[01][0-9]|12[0-7])\\.${OCTET}\\.${OCTET}${AFTER}`)],
  // RFC 1918: 10/8, 172.16/12, 192.168/16.
  [
    'private_ip',
    new RegExp(
      `${BEFORE}(?:10\\.${OCTET}\\.${OCTET}\\.${OCTET}|172\\.(?:1[6-9]|2[0-9]|3[01])\\.${OCTET}\\.${OCTET}|192\\.168\\.${OCTET}\\.${OCTET})${AFTER}`,
    ),
  ],
  // A path under /home (drive mounts and home directories carry the desktop user name).
  ['home_path', /(?<![A-Za-z0-9_.-])\/home\//],
];

/** The first rule a string breaks, or null. */
export function scrubString(text: string): ScrubRule | null {
  for (const [rule, pattern] of RULES) if (pattern.test(text)) return rule;
  return null;
}

/** Thrown by {@link scrubSecrets} when the body is too deep or too large to walk: not a snapshot. */
export class ScrubLimitError extends Error {
  constructor(readonly limit: 'depth' | 'nodes') {
    super(`scrub ${limit} limit exceeded`);
    this.name = 'ScrubLimitError';
  }
}

interface Frame {
  value: unknown;
  path: string;
  depth: number;
}

const IDENTIFIER = /^[A-Za-z_][A-Za-z0-9_]*$/;

/**
 * A path segment for an object key. A key that is a plain identifier is safe to log; any other key (it could be
 * attacker or agent controlled free text, such as a `details` record key) is logged as `<key>`.
 */
function keySegment(parent: string, key: string): string {
  const segment = IDENTIFIER.test(key) && key.length <= 64 && scrubString(key) === null ? key : '<key>';
  return parent === '' ? segment : `${parent}.${segment}`;
}

/**
 * Walks every string value and object key of `body` and returns the first finding, or null when the document is
 * clean. Never throws on content; throws {@link ScrubLimitError} for a document too deep or too large.
 */
export function scrubSecrets(body: unknown): ScrubFinding | null {
  const stack: Frame[] = [{ value: body, path: '', depth: 0 }];
  let visited = 0;
  while (stack.length > 0) {
    const frame = stack.pop() as Frame;
    visited += 1;
    if (visited > SCRUB_MAX_NODES) throw new ScrubLimitError('nodes');
    const { value, path, depth } = frame;
    if (typeof value === 'string') {
      const rule = scrubString(value);
      if (rule) return { path: path || '$', rule };
      continue;
    }
    if (value === null || typeof value !== 'object') continue;
    if (depth >= SCRUB_MAX_DEPTH) throw new ScrubLimitError('depth');
    if (Array.isArray(value)) {
      // Pushed in reverse so the first element is examined first and findings are reported in document order.
      for (let index = value.length - 1; index >= 0; index -= 1) {
        stack.push({ value: value[index], path: `${path}[${index}]`, depth: depth + 1 });
      }
      continue;
    }
    const entries = Object.entries(value as Record<string, unknown>);
    for (let index = entries.length - 1; index >= 0; index -= 1) {
      const [key, child] = entries[index] as [string, unknown];
      const childPath = keySegment(path, key);
      const keyRule = scrubString(key);
      if (keyRule) return { path: childPath, rule: keyRule };
      stack.push({ value: child, path: childPath, depth: depth + 1 });
    }
  }
  return null;
}
