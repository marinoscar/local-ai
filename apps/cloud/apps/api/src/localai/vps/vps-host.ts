// Host names of the stack-agent (docs/specs/cloud-app.md section 4.10). Pure. The CLI has its own copy
// (apps/cli/src/localai/vps-host.ts) and re-checks everything it is told; both carry the same table test.

/** A lab node id: `ai-n` and a number from 1 to 9999 without a leading zero. */
export const NODE_ID_RE = /^ai-n[1-9][0-9]{0,3}$/;

/** `vps.stagingSuffix`: empty (production) or a dash and 1 to 16 lower-case letters or digits. */
export const STAGING_SUFFIX_RE = /^(?:-[a-z0-9]{1,16})?$/;

/** The only host names the agent publishes. */
export const VPS_HOST_RE = /^ai-n[1-9][0-9]{0,3}(?:-[a-z0-9]{1,16})?\.marin\.cr$/;

export const VPS_DOMAIN = 'marin.cr';

export const isStagingSuffix = (value: unknown): value is string => typeof value === 'string' && STAGING_SUFFIX_RE.test(value);

/** `ai-n3` + `-staging` -> `ai-n3-staging.marin.cr`. Throws for a node id or suffix that is not valid. */
export function vpsHostFor(nodeId: string, stagingSuffix: string): string {
  if (!NODE_ID_RE.test(nodeId)) throw new Error('not a lab node id');
  if (!isStagingSuffix(stagingSuffix)) throw new Error('not a valid staging suffix');
  return `${nodeId}${stagingSuffix}.${VPS_DOMAIN}`;
}

/** The host regex AND the equality with `ai-n<N><suffix>.marin.cr` for the configured suffix. */
export function isAllowedVpsHost(host: unknown, nodeId: unknown, stagingSuffix: unknown): boolean {
  if (typeof host !== 'string' || !VPS_HOST_RE.test(host)) return false;
  if (typeof nodeId !== 'string' || !NODE_ID_RE.test(nodeId) || !isStagingSuffix(stagingSuffix)) return false;
  return host === `${nodeId}${stagingSuffix}.${VPS_DOMAIN}`;
}
