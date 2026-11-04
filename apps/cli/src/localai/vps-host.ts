// Host names and the other values the VPS stack-agent puts into DNS records, nginx files and argv
// (docs/specs/cloud-app.md section 4.10). Pure: no I/O. EVERY value that reaches a subprocess argument, an nginx file
// or a URL passes through one of these checks first; a value that fails is refused, never repaired.
//
// The API computes the same host (apps/api/src/localai/vps/vps-host.ts) and sends it in the job's brokered secret. The
// executor does not trust that: it recomputes the host from the job's `nodeId` and the suffix and refuses a mismatch.

/** A lab node id: `ai-n` and a number from 1 to 9999 without a leading zero. */
export const NODE_ID_RE = /^ai-n[1-9][0-9]{0,3}$/;

/** The staging suffix: empty (production) or a dash and 1 to 16 lower-case letters or digits (`-staging`). */
export const STAGING_SUFFIX_RE = /^(?:-[a-z0-9]{1,16})?$/;

/** The only host names this agent publishes: `ai-n<N>[<suffix>].marin.cr`. */
export const VPS_HOST_RE = /^ai-n[1-9][0-9]{0,3}(?:-[a-z0-9]{1,16})?\.marin\.cr$/;

/** The public domain of the lab's node hosts. */
export const VPS_DOMAIN = 'marin.cr';

/** A fixed-text refusal. The message never echoes the value that failed. */
export class VpsInputError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'VpsInputError';
  }
}

export function isNodeId(value: unknown): value is string {
  return typeof value === 'string' && NODE_ID_RE.test(value);
}

export function isStagingSuffix(value: unknown): value is string {
  return typeof value === 'string' && STAGING_SUFFIX_RE.test(value);
}

/** `ai-n3` and `-staging` give `ai-n3-staging.marin.cr`; an empty suffix gives `ai-n3.marin.cr`. */
export function vpsHostFor(nodeId: string, stagingSuffix: string): string {
  if (!isNodeId(nodeId)) throw new VpsInputError('The node id is not a lab node id (ai-n followed by 1 to 9999).');
  if (!isStagingSuffix(stagingSuffix)) throw new VpsInputError('The staging suffix is not valid.');
  return `${nodeId}${stagingSuffix}.${VPS_DOMAIN}`;
}

/**
 * Whether `host` is the host this node may be published under right now: it matches the host regex AND equals
 * `ai-n<N><stagingSuffix>.marin.cr`. A suffix that is not the configured one is refused.
 */
export function isAllowedVpsHost(host: unknown, nodeId: unknown, stagingSuffix: unknown): host is string {
  if (typeof host !== 'string' || !VPS_HOST_RE.test(host)) return false;
  if (!isNodeId(nodeId) || !isStagingSuffix(stagingSuffix)) return false;
  return host === `${nodeId}${stagingSuffix}.${VPS_DOMAIN}`;
}

/** A Tailscale address: IPv4 in the CGNAT block (100.64/10) or IPv6 in the fd7a:115c:a1e0 prefix. */
export function isTailnetAddress(value: unknown): value is string {
  if (typeof value !== 'string') return false;
  const v4 = /^100\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(value);
  if (v4) {
    const [second, third, fourth] = [Number(v4[1]), Number(v4[2]), Number(v4[3])];
    return second >= 64 && second <= 127 && third <= 255 && fourth <= 255;
  }
  return /^fd7a:115c:a1e0(?::[0-9a-f]{1,4}){1,5}$/i.test(value);
}

/** The node's machine name or MagicDNS name: lower-case labels, nothing that could end a directive in nginx. */
export const TAILNET_NAME_RE = /^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?){0,4}$/;

/** A Route53 hosted zone id. */
export const HOSTED_ZONE_ID_RE = /^Z[A-Z0-9]{1,31}$/;

/** A Route53 change id. */
export const CHANGE_ID_RE = /^C[A-Z0-9]{1,31}$/;

/** A container name that cannot be read as an option (`-x`) or carry a path. */
export const CONTAINER_NAME_RE = /^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$/;

/** A plain e-mail address for the certificate registration. */
export const EMAIL_RE = /^[A-Za-z0-9._%+-]{1,64}@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?){1,4}$/;

/** A public DNS name such as `ai.marin.cr` (the VPS's own name, whose A record is the publish target). */
export const DNS_NAME_RE = /^(?=.{4,253}$)(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$/;

/** A public IPv4 address: not private, shared (CGNAT), loopback, link-local, multicast or reserved. */
export function isPublicIpv4(value: string): boolean {
  const match = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(value);
  if (!match) return false;
  const octets = match.slice(1).map(Number);
  if (octets.some((octet) => octet > 255)) return false;
  const [a, b] = octets as [number, number, number, number];
  if (a === 0 || a === 10 || a === 127 || a >= 224) return false;
  if (a === 100 && b >= 64 && b <= 127) return false;
  if (a === 169 && b === 254) return false;
  if (a === 172 && b >= 16 && b <= 31) return false;
  if (a === 192 && b === 168) return false;
  if (a === 192 && b === 0) return false;
  if (a === 198 && (b === 18 || b === 19)) return false;
  return true;
}
