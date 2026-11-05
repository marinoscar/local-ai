// A gateway lab for the limits and usage specs: the Fastify gateway scope over a REAL `GatewayService`, `AttemptLoop`,
// `UpstreamClient` and in-memory `RoutingTable`, fake llama-servers on real local sockets, and the REAL `RateLimiter`,
// `DailyUsageLedger` and `UsageRecorder` (GW-08) over a store the spec chooses (a recording fake, or the Prisma store).
import { randomBytes } from 'node:crypto';
import { request as httpRequest, type IncomingHttpHeaders } from 'node:http';
import type { AddressInfo } from 'node:net';

import { AttemptLoop } from '../../../src/localai/gateway/attempts';
import type { GatewayKeyRecord } from '../../../src/localai/gateway/auth';
import { GatewayError } from '../../../src/localai/gateway/errors';
import { GatewayService } from '../../../src/localai/gateway/gateway.service';
import { buildGateway, type TestGateway } from '../../../src/localai/gateway/gateway.testkit';
import { proxyHandlers } from '../../../src/localai/gateway/handlers';
import { RateLimiter } from '../../../src/localai/gateway/limits/rate-limiter';
import { DEFAULT_MODEL_POLICY, type GatewayModelResolver, type ResolvedGatewayModel } from '../../../src/localai/gateway/model-resolver';
import { keyAllows } from '../../../src/localai/gateway/models/resolve';
import { DEFAULT_GATEWAY_TIMEOUTS, type ProxySettings } from '../../../src/localai/gateway/proxy-settings';
import type { SourceNodeRow } from '../../../src/localai/gateway/routing/build-candidates';
import { FakeBus, FakeClock, model, node } from '../../../src/localai/gateway/routing/routing.fixtures';
import { RoutingTable } from '../../../src/localai/gateway/routing/routing-table';
import { StreamProxy } from '../../../src/localai/gateway/stream/stream-proxy';
import { UpstreamClient } from '../../../src/localai/gateway/upstream/client';
import { DailyUsageLedger, type DailyUsageReader } from '../../../src/localai/gateway/usage/usage-ledger';
import { UsageRecorder } from '../../../src/localai/gateway/usage/usage-recorder';
import type { UsageStore } from '../../../src/localai/gateway/usage/usage-store';
import { FakeLlama, okBehaviour, type FakeLlamaBehaviour } from '../../fakes/fake-llama';
import { modelsHandlers } from '../../../src/localai/gateway/models-route';

export class LabResolver implements GatewayModelResolver {
  async resolve(requested: string, key: { allowedModels: string[] }): Promise<ResolvedGatewayModel> {
    if (!['model-a', 'embed-a'].includes(requested)) throw new GatewayError('LAI-GW-020');
    if (!keyAllows(key, requested, { id: requested })) throw new GatewayError('LAI-GW-012');
    return { modelId: requested, requestedModel: requested, policy: DEFAULT_MODEL_POLICY, roles: requested === 'embed-a' ? ['embedding'] : ['chat'], inApprovedCatalog: true };
  }
}

export interface LabOptions {
  nodes?: Array<{ id: string; behaviour?: FakeLlamaBehaviour }>;
  keyOverrides?: Partial<GatewayKeyRecord>;
  store: UsageStore;
  reader?: DailyUsageReader;
  now?: () => number;
  /** Replaces the recorder's own timer: no timer, the spec flushes. */
  flushIntervalMs?: number;
}

export interface Lab {
  gw: TestGateway;
  limiter: RateLimiter;
  recorder: UsageRecorder;
  ledger: DailyUsageLedger;
  llamas: Record<string, FakeLlama>;
  table: RoutingTable;
  post(path: string, body: unknown): Promise<{ status: number; headers: Record<string, unknown>; json: any; text: string }>;
  get(path: string): Promise<{ status: number; headers: Record<string, unknown>; json: any; text: string }>;
  /** A streaming request over a real socket (the stream path hijacks the reply, so `inject` cannot carry it). */
  stream(path: string, body: unknown): LabStream;
  cleanup(): Promise<void>;
}

export interface LabStream {
  /** Resolves with the response head as soon as it arrives. */
  head: Promise<{ status: number; headers: IncomingHttpHeaders }>;
  /** Resolves when the response ended (or the socket closed after `abort()`), with everything received. */
  done: Promise<{ status: number; headers: IncomingHttpHeaders; text: string }>;
  /** Everything received so far. */
  received(): string;
  abort(): void;
}

export async function buildLab(options: LabOptions): Promise<Lab> {
  const llamas: Record<string, FakeLlama> = {};
  const origins = new Map<string, string>();
  const nodeKeys = new Map<string, string | null>();
  const rows: SourceNodeRow[] = [];
  for (const [index, entry] of (options.nodes ?? [{ id: 'ai-n1' }]).entries()) {
    const llama = await FakeLlama.start(entry.behaviour ?? okBehaviour);
    llamas[entry.id] = llama;
    origins.set(`${entry.id}.example.test`, llama.origin);
    nodeKeys.set(entry.id, `llama-${randomBytes(16).toString('hex')}`);
    rows.push(node(entry.id, [model('model-a'), model('embed-a')], { gatewayPriority: 10 + index }));
  }
  const table = new RoutingTable({ load: async () => ({ rows, settings: { queueFactor: 4 } }) }, new FakeBus() as never, new FakeClock(), { timers: false, rebuildIntervalMs: 10_000 });
  table.onModuleInit();
  await table.rebuild();

  const client = new UpstreamClient({ version: 'test', resolveOrigin: (host) => origins.get(host) ?? 'http://127.0.0.1:1' });
  const settings: ProxySettings = { timeouts: { ...DEFAULT_GATEWAY_TIMEOUTS, connectMs: 500, firstByteMs: 3000, idleMs: 3000, totalMs: 8000, nonStreamTotalMs: 8000 }, maxAttempts: 2 };
  const ledger = new DailyUsageLedger(options.reader ?? { read: async () => 0 }, options.now);
  const recorder = new UsageRecorder({ store: options.store, ledger, ...(options.now ? { now: options.now } : {}), flushIntervalMs: options.flushIntervalMs ?? 2000 });
  const limiter = new RateLimiter({ ledger, ...(options.now ? { now: options.now } : {}) });
  const loop = new AttemptLoop({ routing: table, keys: { get: async (id) => nodeKeys.get(id) ?? null }, transport: client, log: { warn: () => undefined } });
  const service = new GatewayService(new LabResolver(), limiter, recorder, new StreamProxy(), { read: async () => settings }, loop);
  const listing = {
    list: async () => ({ object: 'list', data: [{ id: 'model-a', object: 'model' }] }),
    find: async (_key: unknown, id: string) => (id === 'model-a' ? { id, object: 'model' } : null),
  };
  const gw = await buildGateway({
    handlers: { ...proxyHandlers(service), ...modelsHandlers(listing as never) },
    ...(options.keyOverrides ? { keyOverrides: options.keyOverrides } : {}),
    rateLimitHeaders: limiter,
    usage: recorder,
  });
  const auth = { authorization: `Bearer ${gw.token}`, 'content-type': 'application/json' };
  const parse = (res: { statusCode: number; headers: Record<string, unknown>; body: string }) => {
    let json: unknown = null;
    try {
      json = JSON.parse(res.body);
    } catch {
      json = null;
    }
    return { status: res.statusCode, headers: res.headers, json, text: res.body };
  };
  return {
    gw,
    limiter,
    recorder,
    ledger,
    llamas,
    table,
    async post(path, body) {
      return parse(await gw.app.inject({ method: 'POST', url: `${gw.prefix}${path}`, headers: auth, payload: typeof body === 'string' ? body : JSON.stringify(body) }));
    },
    async get(path) {
      return parse(await gw.app.inject({ method: 'GET', url: `${gw.prefix}${path}`, headers: { authorization: auth.authorization } }));
    },
    stream(path, body) {
      let text = '';
      let status = 0;
      let headers: IncomingHttpHeaders = {};
      let request: ReturnType<typeof httpRequest> | null = null;
      let resolveHead!: (value: { status: number; headers: IncomingHttpHeaders }) => void;
      const head = new Promise<{ status: number; headers: IncomingHttpHeaders }>((resolve) => {
        resolveHead = resolve;
      });
      const done = new Promise<{ status: number; headers: IncomingHttpHeaders; text: string }>((resolve) => {
        void (async () => {
          if (!gw.app.server.listening) await gw.app.listen({ port: 0, host: '127.0.0.1' });
          const port = (gw.app.server.address() as AddressInfo).port;
          request = httpRequest({ host: '127.0.0.1', port, method: 'POST', path: `${gw.prefix}${path}`, headers: auth }, (response) => {
            status = response.statusCode ?? 0;
            headers = response.headers;
            resolveHead({ status, headers });
            response.setEncoding('utf8');
            response.on('data', (chunk: string) => (text += chunk));
            response.on('end', () => resolve({ status, headers, text }));
            response.on('close', () => resolve({ status, headers, text }));
          });
          request.on('error', () => resolve({ status, headers, text }));
          request.end(typeof body === 'string' ? body : JSON.stringify(body));
        })();
      });
      return { head, done, received: () => text, abort: () => request?.destroy() };
    },
    async cleanup() {
      await recorder.onModuleDestroy();
      await gw.app.close();
      await client.close();
      table.onModuleDestroy();
      await Promise.all(Object.values(llamas).map((llama) => llama.close()));
    },
  };
}
