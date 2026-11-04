// Node ids and public hosts of the Add-node wizard (docs/specs/cloud-app.md section 4.8). Pure: no I/O.

/** A lab node id: `ai-n` and a number from 1 to 9999 without a leading zero (agent.md section 3.4). */
export const NODE_ID_RE = /^ai-n[1-9][0-9]{0,3}$/;

/** The domain the lab's public hosts live under: `ai-n4` is published as `ai-n4.marin.cr`. */
export const DEFAULT_PUBLIC_DOMAIN = 'marin.cr';

/**
 * A DNS name the cloud may probe from its own container: lower-case labels, at least two of them, a TLD that is not
 * all digits (so an IPv4 address, `localhost` and a bare name never pass). It is put in URLs and in nginx files.
 */
export const PUBLIC_HOST_RE = /^(?=.{4,253}$)(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$/;

/** The largest number a node id can carry. */
export const MAX_NODE_NUMBER = 9999;

export function isNodeId(value: string): boolean {
  return NODE_ID_RE.test(value);
}

/** `ai-n4` -> `ai-n4.marin.cr`. */
export function derivePublicHost(nodeId: string, domain: string = DEFAULT_PUBLIC_DOMAIN): string {
  return `${nodeId}.${domain}`;
}

/**
 * The suggestion for the next node: `ai-n<max+1>` over EVERY node id ever used, retired ones included (an id is never
 * reused). Ids that are not in the lab's shape are ignored. Returns null when the numbers are used up.
 */
export function nextFreeNodeId(existing: readonly string[]): string | null {
  let max = 0;
  for (const id of existing) {
    if (!isNodeId(id)) continue;
    max = Math.max(max, Number(id.slice('ai-n'.length)));
  }
  return max + 1 > MAX_NODE_NUMBER ? null : `ai-n${max + 1}`;
}

/** The placeholder the committed templates use for this node's tailnet address: `ai-n4` -> `<TAILNET_IP_AI_N4>`. */
export function tailnetIpPlaceholder(nodeId: string): string {
  return `<TAILNET_IP_${nodeId.toUpperCase().replace(/-/g, '_')}>`;
}
