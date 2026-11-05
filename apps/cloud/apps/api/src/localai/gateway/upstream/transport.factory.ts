// Chooses the upstream transport from `LOCALAI_GATEWAY_UPSTREAM` (docs/specs/gateway.md sections 3.5 and 3.7).
// `public` is the default; `tailnet` skips the VPS's own nginx and talks to the node over Tailscale. The mode is read
// once, when the provider is built, so switching back needs a restart and nothing else (no data depends on it).
import { Logger } from '@nestjs/common';

import { fakeUpstreamFromEnv, FAKE_UPSTREAM_CONNECTIONS } from '../routing/fake-upstream.seed';
import { UpstreamClient, type UpstreamTransport } from './client';
import { TailnetTargets, type TailnetSuffixSource } from './targets';

export type UpstreamMode = 'public' | 'tailnet';

export function upstreamModeFromEnv(env: NodeJS.ProcessEnv = process.env): UpstreamMode {
  return (env.LOCALAI_GATEWAY_UPSTREAM ?? 'public').trim().toLowerCase() === 'tailnet' ? 'tailnet' : 'public';
}

export function createUpstreamTransport(options: { version: string; env?: NodeJS.ProcessEnv; tailnetSuffix: TailnetSuffixSource }): UpstreamTransport {
  const logger = new Logger('GatewayUpstream');
  // The load-test flag (GW-12) wins over both modes; refused in production. Every call goes to the echo server.
  const fake = fakeUpstreamFromEnv(options.env);
  if (fake) return new UpstreamClient({ version: options.version, resolveOrigin: () => fake.origin, connections: FAKE_UPSTREAM_CONNECTIONS });
  if (upstreamModeFromEnv(options.env) === 'tailnet') {
    logger.log('upstream mode: tailnet');
    return new UpstreamClient({
      version: options.version,
      targets: new TailnetTargets({ suffix: options.tailnetSuffix, warn: (line) => logger.warn(line) }),
    });
  }
  return new UpstreamClient({ version: options.version });
}
