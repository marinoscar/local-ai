import { readFileSync } from 'node:fs';

import { APP_NAME, CLI_NAME, REPO_SLUG } from '@app/shared';
import type { CreateCliOptions } from '@marinoscar/platform-cli';

import { vpsExecutors } from './localai/vps-publish.executor.js';

/** This CLI's version: apps/cli/package.json, or `0.0.0-unknown` in an unusual install. */
function readVersion(): string {
  try {
    const manifest = JSON.parse(readFileSync(new URL('../package.json', import.meta.url), 'utf8')) as { version?: string };
    return manifest.version || '0.0.0-unknown';
  } catch {
    return '0.0.0-unknown';
  }
}

/**
 * The CLI is `@marinoscar/platform-cli`, composed with this app's identity
 * (packages/shared/identity.json): the binary name seeds `~/.<name>/` and the
 * `<NAME>_` environment prefix. Add app commands through `extraCommands`.
 */
export const APP_CLI_OPTIONS: CreateCliOptions = {
  identity: { name: CLI_NAME, displayName: `${APP_NAME} CLI`, productName: APP_NAME, repoSlug: REPO_SLUG },
  version: readVersion(),
  extraCommands: [],
  // The VPS stack-agent's two job types (docs/specs/cloud-app.md section 4.10). They are claimed only by a worker
  // started with `--types` naming them (runbook 10 part 10); every other worker leaves them alone.
  nodeExecutors: vpsExecutors(),
  // The OpenAI-compatible gateway (docs/specs/gateway.md section 3.1): the edge vhost must not buffer /v1/ nor cut a
  // streamed completion at 60 s. 1800 s matches the gateway's own total timeout (nginx gateway.conf).
  proxy: { streamPaths: [{ path: '/v1/', timeoutSeconds: 1800 }] },
};
