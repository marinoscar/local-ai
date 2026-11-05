// The fake-upstream TEST flag of the gateway (docs/specs/gateway.md section 9 "Load testing", issue GW-12).
//
// WHAT IT IS. With `LOCALAI_GATEWAY_FAKE_UPSTREAM=1` (and `NODE_ENV` not `production`) the gateway runs against ONE
// synthetic node whose "llama-server" is the echo server `test/load/fake-upstream.mjs` on
// `LOCALAI_GATEWAY_FAKE_UPSTREAM_URL` (default `http://127.0.0.1:18999`). The k6 script measures what the gateway adds
// per event against it, without a GPU and without touching a real node.
//
// WHAT IT REPLACES, AND ONLY IN MEMORY. Five seams, each chosen by a function below when its provider is built:
//   ROUTING_SOURCE         one routable node `fake-load-node` serving the model `fake-load-model` (the table is rebuilt
//                          from it; the database rows of real nodes are never read, so none is routed to or changed)
//   NodeKeyProvider        a random key made at start-up (the echo server needs only that a Bearer header is present)
//   HEALTH_CLIENT          the endpoint probe checks `<fake url>/health` instead of `https://<publicHost>/health`
//   ENDPOINT_ALERTS        raises nothing (the synthetic node has no row an alert could point at)
//   APPROVED_CATALOG_STORE a one-model approved catalog, so the model resolves without an approved catalog in the database
// and, in `upstream/transport.factory.ts`, the upstream transport (every call goes to the fake origin). Nothing is written
// to the database by any of it. Keys, limits and usage still use the real database: the k6 run needs a real service key.
//
// SAFETY. The flag is a test tool and must never be on in a deployment:
//   - `assertFakeUpstreamAllowed` throws `FakeUpstreamRefusedError` when the flag is set and `NODE_ENV` is `production`
//     (any spelling of the flag counts, so `=true` or `=yes` is refused too, not only `=1`). `main.ts` calls it before
//     anything is built, and every `fakeUpstreamFromEnv` caller goes through the same check, so a path that skips `main.ts`
//     (a spec, a script that builds the app) still refuses.
//   - the URL must be an `http://` origin on a loopback address (`127.0.0.0/8`, `localhost`, `::1`), so the flag can never
//     point the gateway at another machine;
//   - the flag is in no compose file, env example or Dockerfile (a spec greps for it: `test/load/load-harness.spec.ts`);
//   - the API logs a WARNING at start-up while it is active.
import { randomBytes } from 'node:crypto';
import { isIP } from 'node:net';

import { Logger } from '@nestjs/common';

import type { SourceNodeRow } from './build-candidates';
import { noopEndpointAlerts, type EndpointAlertsPort } from './endpoint-alerts.port';
import { fetchHealthClient, type HealthClient } from './probe';
import type { RoutingSource, RoutingSourceData } from './routing-source';
import { DEFAULT_QUEUE_FACTOR } from './routing.types';
import { NodeKeyProvider } from './node-key.provider';
import type { ApprovedCatalogHead, ApprovedCatalogStore } from '../models/catalog-hints';

/** The switch. Any value but empty, `0` or `false` counts as "on" (and is refused in production). */
export const FAKE_UPSTREAM_FLAG = 'LOCALAI_GATEWAY_FAKE_UPSTREAM';
export const FAKE_UPSTREAM_URL_VAR = 'LOCALAI_GATEWAY_FAKE_UPSTREAM_URL';
export const DEFAULT_FAKE_UPSTREAM_URL = 'http://127.0.0.1:18999';

/** The synthetic node and model. Invented ids; the model id satisfies the catalog's id rule. */
export const FAKE_NODE_ID = 'fake-load-node';
export const FAKE_NODE_HOST = 'fake-load-node.example.test';
export const FAKE_MODEL_ID = 'fake-load-model';
/** `--parallel` of the synthetic model: with `queueFactor` 4 the node saturates at 512 in flight, above the 200 of the scenario. */
export const FAKE_NODE_SLOTS = 128;
/**
 * undici connections per origin towards the fake node. Production keeps 32 (`UPSTREAM_CONNECTIONS`), which suits a node
 * serving a handful of parallel requests; one synthetic node standing in for a lab would otherwise queue 168 of 200 streams.
 */
export const FAKE_UPSTREAM_CONNECTIONS = 512;

/** The flag is set, or `NODE_ENV` forbids it. Thrown at start-up; the API does not boot. */
export class FakeUpstreamRefusedError extends Error {
  constructor(reason: string) {
    super(reason);
    this.name = 'FakeUpstreamRefusedError';
  }
}

/** Is the flag on? Any non-blank value other than `0` and `false`. */
export function fakeUpstreamRequested(env: NodeJS.ProcessEnv = process.env): boolean {
  const value = (env[FAKE_UPSTREAM_FLAG] ?? '').trim().toLowerCase();
  return value !== '' && value !== '0' && value !== 'false';
}

export function isProductionEnv(env: NodeJS.ProcessEnv = process.env): boolean {
  return (env.NODE_ENV ?? '').trim().toLowerCase() === 'production';
}

/**
 * Refuses to start with the flag in a production environment.
 *
 * @throws FakeUpstreamRefusedError when {@link fakeUpstreamRequested} and `NODE_ENV` is `production`.
 */
export function assertFakeUpstreamAllowed(env: NodeJS.ProcessEnv = process.env): void {
  if (fakeUpstreamRequested(env) && isProductionEnv(env)) {
    throw new FakeUpstreamRefusedError(`${FAKE_UPSTREAM_FLAG} must not be set in production (NODE_ENV=production): it replaces every real node with a synthetic one`);
  }
}

function isLoopbackHost(hostname: string): boolean {
  const host = hostname.startsWith('[') && hostname.endsWith(']') ? hostname.slice(1, -1) : hostname;
  if (host === 'localhost') return true;
  if (isIP(host) === 4) return host.startsWith('127.');
  return isIP(host) === 6 && (host === '::1' || host === '0:0:0:0:0:0:0:1');
}

/** The origin (`http://127.0.0.1:18999`) of a fake upstream URL, or null when it is not a plain loopback http origin. Pure. */
export function parseFakeUpstreamOrigin(raw: string): string | null {
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    return null;
  }
  if (url.protocol !== 'http:' || !isLoopbackHost(url.hostname)) return null;
  if (url.username !== '' || url.password !== '' || url.search !== '' || url.hash !== '' || (url.pathname !== '/' && url.pathname !== '')) return null;
  return url.origin;
}

/** The models.yaml of the one-model approved catalog (the layout `parseModelsYaml` reads). */
export const FAKE_MODELS_YAML = `models:
  - id: ${FAKE_MODEL_ID}
    display_name: Fake load-test model
    roles: [chat]
    client_hints:
      max_output_tokens: 512
      reasoning: false
    llama:
      ctx: 8192
`;

/** What the flag turns on. Built once per provider; holds no secret but the random key of the synthetic node. */
export interface FakeUpstream {
  /** `http://127.0.0.1:<port>`: where every call, and the probe, goes. */
  origin: string;
}

/**
 * The fake upstream described by `env`, or null when the flag is off.
 *
 * @throws FakeUpstreamRefusedError in production, or when {@link FAKE_UPSTREAM_URL_VAR} is not a loopback `http://` origin.
 */
export function fakeUpstreamFromEnv(env: NodeJS.ProcessEnv = process.env): FakeUpstream | null {
  if (!fakeUpstreamRequested(env)) return null;
  assertFakeUpstreamAllowed(env);
  const raw = (env[FAKE_UPSTREAM_URL_VAR] ?? '').trim() || DEFAULT_FAKE_UPSTREAM_URL;
  const origin = parseFakeUpstreamOrigin(raw);
  if (origin === null) {
    throw new FakeUpstreamRefusedError(`${FAKE_UPSTREAM_URL_VAR} must be an http:// origin on a loopback address (127.0.0.1, localhost or ::1), with no path`);
  }
  return { origin };
}

let announced = false;

/** One loud line, once per process, whichever seam is built first. */
function announce(fake: FakeUpstream): void {
  if (announced) return;
  announced = true;
  new Logger('GatewayFakeUpstream').warn(`${FAKE_UPSTREAM_FLAG} is ON: every request goes to the synthetic node "${FAKE_NODE_ID}" at ${fake.origin}; real nodes are not routed to. TEST ONLY.`);
}

/** A routing source of one routable node. The timestamps follow `now`, so the node never goes stale. */
export class FakeRoutingSource implements RoutingSource {
  async load(now: Date): Promise<RoutingSourceData> {
    const row: SourceNodeRow = {
      nodeId: FAKE_NODE_ID,
      publicHost: FAKE_NODE_HOST,
      servePattern: 'A',
      tailnetIp: null,
      tailnetMachine: null,
      gatewayPriority: 100,
      gatewayEnabled: true,
      phase: 'enrolled',
      mode: 'online',
      lastStateAt: now,
      keyReportedAt: now,
      eab: 'healthy',
      models: [{ modelId: FAKE_MODEL_ID, present: true, verified: true, loadable: true, status: 'loaded', slots: FAKE_NODE_SLOTS, ctx: 8192, inputModalities: ['text'] }],
    };
    return { rows: [row], settings: { queueFactor: DEFAULT_QUEUE_FACTOR } };
  }
}

/** The node key of the synthetic node: random per process, never stored, never logged. */
export class FakeNodeKeyProvider extends NodeKeyProvider {
  private readonly material = `fake-${randomBytes(16).toString('hex')}`;

  constructor() {
    super(undefined as never);
  }

  override async get(nodeId: string): Promise<string | null> {
    return nodeId === FAKE_NODE_ID ? this.material : null;
  }
}

/** The probe checks the echo server's `/health` for the synthetic node. */
export class FakeHealthClient implements HealthClient {
  constructor(
    private readonly origin: string,
    private readonly inner: HealthClient = fetchHealthClient,
  ) {}

  check(_url: string, timeoutMs: number): ReturnType<HealthClient['check']> {
    return this.inner.check(`${this.origin}/health`, timeoutMs);
  }
}

/** An approved catalog of one model, approved at construction time. */
export class FakeCatalogStore implements ApprovedCatalogStore {
  private readonly approvedAt = new Date();

  async head(): Promise<ApprovedCatalogHead | null> {
    return { version: 'fake-load-test', approvedAt: this.approvedAt };
  }

  async modelsYaml(version: string): Promise<string | null> {
    return version === 'fake-load-test' ? FAKE_MODELS_YAML : null;
  }
}

// ---- the selectors the modules call: the real provider unless the flag is on ---------------------------------------

export function routingSourceFor(real: RoutingSource, env: NodeJS.ProcessEnv = process.env): RoutingSource {
  const fake = fakeUpstreamFromEnv(env);
  if (!fake) return real;
  announce(fake);
  return new FakeRoutingSource();
}

export function nodeKeysFor(real: NodeKeyProvider, env: NodeJS.ProcessEnv = process.env): NodeKeyProvider {
  const fake = fakeUpstreamFromEnv(env);
  if (!fake) return real;
  announce(fake);
  return new FakeNodeKeyProvider();
}

export function healthClientFor(real: HealthClient, env: NodeJS.ProcessEnv = process.env): HealthClient {
  const fake = fakeUpstreamFromEnv(env);
  if (!fake) return real;
  announce(fake);
  return new FakeHealthClient(fake.origin);
}

export function endpointAlertsFor(real: EndpointAlertsPort, env: NodeJS.ProcessEnv = process.env): EndpointAlertsPort {
  const fake = fakeUpstreamFromEnv(env);
  if (!fake) return real;
  announce(fake);
  return noopEndpointAlerts;
}

export function catalogStoreFor(real: ApprovedCatalogStore, env: NodeJS.ProcessEnv = process.env): ApprovedCatalogStore {
  const fake = fakeUpstreamFromEnv(env);
  if (!fake) return real;
  announce(fake);
  return new FakeCatalogStore();
}
