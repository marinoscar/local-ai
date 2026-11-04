// Fastify's default JSON body limit is 1 MiB, but a catalog draft may weigh 2 MB of file content
// (docs/specs/cloud-app.md section 4.6) and JSON escaping of quotes and newlines inflates it. This lifts the limit for
// that one route only; every other route keeps the default. The service still enforces the 2 MB on the CONTENT and
// answers 413 itself (`catalog.service.ts`).
import type { FastifyInstance } from 'fastify';

/** 2 MB of content, doubled for JSON escaping, plus the envelope. */
export const CATALOG_DRAFT_BODY_LIMIT = 6 * 1024 * 1024;

const DRAFT_ROUTE = /\/localai\/catalog\/versions$/;

/** Must run before the routes are registered (before `app.init()`), like the other `onRoute` hooks. */
export function raiseCatalogDraftBodyLimit(instance: FastifyInstance): void {
  instance.addHook('onRoute', (route) => {
    const methods = Array.isArray(route.method) ? route.method : [route.method];
    if (methods.includes('POST') && DRAFT_ROUTE.test(route.url)) route.bodyLimit = CATALOG_DRAFT_BODY_LIMIT;
  });
}
