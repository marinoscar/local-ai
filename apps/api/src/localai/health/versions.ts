// Version comparisons for the "behind" flags of a node card (docs/specs/cloud-app.md section 3.9). PURE.

/** `0.4.1` or `v0.4.1` (an optional `-pre`/`+build` suffix is ignored) as numbers; null when it is not a version. */
export function parseSemver(value: string | null | undefined): [number, number, number] | null {
  if (typeof value !== 'string') return null;
  const match = /^v?(\d{1,6})\.(\d{1,6})\.(\d{1,6})(?:[-+].*)?$/.exec(value.trim());
  if (!match) return null;
  return [Number(match[1]), Number(match[2]), Number(match[3])];
}

/**
 * `agentVersion < latestVersion` by semver. False when either side is unknown or not a version: an unreadable
 * version must never raise a "behind" flag.
 */
export function isAgentBehind(agentVersion: string | null, latestVersion: string | null): boolean {
  const agent = parseSemver(agentVersion);
  const latest = parseSemver(latestVersion);
  if (agent === null || latest === null) return false;
  for (let index = 0; index < 3; index += 1) {
    if (agent[index]! !== latest[index]!) return agent[index]! < latest[index]!;
  }
  return false;
}

/**
 * `catalogVersion != approved`. False when no catalog is approved yet or the node has not reported one (nothing to
 * compare). Spec 3.9 says "node catalog != approved version"; a different version is behind, not ahead, because a
 * node only ever receives an approved catalog.
 */
export function isCatalogBehind(nodeCatalogVersion: string | null, approvedVersion: string | null): boolean {
  if (nodeCatalogVersion === null || approvedVersion === null) return false;
  return nodeCatalogVersion !== approvedVersion;
}
