// Strong ETag handling for the node catalog route (docs/specs/cloud-app.md section 3.6).
//
// The ETag is the `bundleSha256` in double quotes. `If-None-Match` is compared STRONGLY (RFC 9110 section 8.8.3.2): a
// weak validator (`W/"..."`) never matches, `*` matches any current representation.

export function etagOf(bundleSha256: string): string {
  return `"${bundleSha256}"`;
}

/** Whether the request's `If-None-Match` names the current representation. */
export function ifNoneMatchMatches(header: string | string[] | undefined, bundleSha256: string): boolean {
  if (header === undefined) return false;
  const value = Array.isArray(header) ? header.join(',') : header;
  const current = etagOf(bundleSha256);
  return value.split(',').some((candidate) => {
    const token = candidate.trim();
    return token === '*' || token === current;
  });
}
