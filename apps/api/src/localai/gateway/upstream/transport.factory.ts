// Chooses the upstream transport from `LOCALAI_GATEWAY_UPSTREAM` (docs/specs/gateway.md sections 3.5 and 3.7).
// `public` is the default and the only mode built so far; `tailnet` is GW-10. Until then a `tailnet` setting is logged
// and public mode is used, which always works (it is the same path the browsers and the nodes' own checks use).
import { Logger } from '@nestjs/common';

import { UpstreamClient, type UpstreamTransport } from './client';

export type UpstreamMode = 'public' | 'tailnet';

export function upstreamModeFromEnv(env: NodeJS.ProcessEnv = process.env): UpstreamMode {
  return (env.LOCALAI_GATEWAY_UPSTREAM ?? 'public').trim().toLowerCase() === 'tailnet' ? 'tailnet' : 'public';
}

export function createUpstreamTransport(options: { version: string; env?: NodeJS.ProcessEnv }): UpstreamTransport {
  if (upstreamModeFromEnv(options.env) === 'tailnet') {
    new Logger('GatewayUpstream').warn('LOCALAI_GATEWAY_UPSTREAM=tailnet is not built yet, using public mode');
  }
  return new UpstreamClient({ version: options.version });
}
