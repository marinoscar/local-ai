// Route-level body limits of the node-facing Local AI routes (docs/specs/cloud-app.md section 3.6).
//
// Nest has no decorator for Fastify's per-route `bodyLimit`, so it is set the supported Fastify way: an `onRoute`
// hook that fills `bodyLimit` for the matching route as it is registered. It must be installed BEFORE the app is
// initialised (routes register at `init()`): `createApp()` in `src/main.ts` calls it right after the app is built,
// and a spec that builds the app itself calls it before `app.init()`. Fastify then answers `413` itself, before the
// body is parsed, for a declared `content-length` over the limit and while streaming a chunked one.
import type { FastifyInstance } from 'fastify';

import { LOCALAI_STATE_BODY_LIMIT_BYTES } from '../contracts/state.schema';

/** `[method, URL tail, limit in bytes]`. The tail is matched against the route's registered URL (with `:id`). */
const LIMITS: ReadonlyArray<readonly [string, RegExp, number]> = [
  ['POST', /\/nodes\/:[A-Za-z]+\/localai\/state$/, LOCALAI_STATE_BODY_LIMIT_BYTES],
];

/** The limit for a registered route, or null when it has none of its own (the server default applies). */
export function nodeLinkBodyLimitFor(method: string, url: string): number | null {
  for (const [limitMethod, pattern, limit] of LIMITS) {
    if (method.toUpperCase() === limitMethod && pattern.test(url)) return limit;
  }
  return null;
}

/** Installs the `onRoute` hook on the Fastify instance (idempotent per instance is the caller's concern: call once). */
export function registerNodeLinkBodyLimits(instance: FastifyInstance): void {
  instance.addHook('onRoute', (routeOptions) => {
    const methods = Array.isArray(routeOptions.method) ? routeOptions.method : [routeOptions.method];
    for (const method of methods) {
      const limit = nodeLinkBodyLimitFor(method, routeOptions.url);
      if (limit !== null) routeOptions.bodyLimit = limit;
    }
  });
}
